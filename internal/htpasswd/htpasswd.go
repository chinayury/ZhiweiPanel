// Package htpasswd 生成 nginx auth_basic 使用的口令文件。
//
// 为什么需要它：站点配置里有 Access.AuthEnabled / AuthUser / AuthPass
// 三个字段，但早期版本**从来没有把它们渲染进 nginx**——
// 界面上"访问认证"这一栏配了完全不生效，属于典型的功能残缺。
//
// 口令文件格式用 **apr1**（Apache 的 MD5 变体，nginx 原生支持）：
//
//	$apr1$<盐>$<22 位摘要>
//
// 为什么不用 bcrypt：nginx 的 ngx_crypt 不支持 bcrypt，
// 装了也认不出来。apr1 是 htpasswd 的事实标准，nginx / Apache 通用。
//
// 为什么不用明文：明文口令文件一旦泄露（备份、误放到 web 根目录、
// 日志），等于站点密码公开。apr1 是带盐的慢哈希，离线爆破成本高得多。
//
// 本包零外部依赖（只用标准库 crypto/md5 + crypto/rand）。
//
// # 算法出处
//
// apr1 = Apache 的 apr_md5_encode，与 OpenSSL 的 `passwd -apr1`、
// Apache 的 `htpasswd -m` 输出完全一致。实现严格对照 Apache 的
// 三个阶段（初值 → 摘要编排 → 1000 次迭代），并由 TestHashMatchesOpenSSL
// 用 OpenSSL 生成的权威向量锁定。
//
// ⚠️ 这套算法有三个极易写错、且**错了不报错**的地方：
//
//  1. 摘要的字节编排是 crypt(3) 的**交错序**，不是常见的 3 字节分组
//     （后者是 SHA-crypt $5$/$6$ 的排法，用在这里能算出 22 个字符，
//     但 nginx 会永远返回 401）。
//  2. 1000 次迭代的每一轮**有四项**（口令/摘要 + 盐 + 口令 + 摘要/口令），
//     漏掉最后一项结果不对。
//  3. 初值阶段的口令重复段用的是 md5(口令+盐+口令) 的**前 min(n,16) 字节**，
//     且必须追加在 "口令+magic+盐" 之后，不能单独做一次摘要。
package htpasswd

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"strings"
)

// alphabet 是 crypt(3) 家族使用的 64 字符表。
const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// magic 是 apr1 的固定前缀。
const magic = "$apr1$"

// saltChars 盐字符集：去掉容易混淆的字符（0/O、1/l/I）。
const saltChars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Salt 生成一个随机盐（8 位）。
//
// 随机盐保证同一个口令两次生成的哈希不同，
// 攻击者无法用"相同哈希 = 相同口令"做彩虹表匹配。
//
// 用拒绝采样而不是取模，是为了避免取模带来的字符分布偏斜
// （256 不是 56 的整数倍，直接取模会让前 32 个字符各多分到约 0.57% 概率）。
func Salt() (string, error) {
	out := make([]byte, 0, 8)
	buf := make([]byte, 1)
	for len(out) < 8 {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成盐失败: %w", err)
		}
		// 256 % 56 = 32，超出部分丢弃重取
		if int(buf[0]) >= 256-(256%len(saltChars)) {
			continue
		}
		out = append(out, saltChars[int(buf[0])%len(saltChars)])
	}
	return string(out), nil
}

// Hash 用 apr1 算法计算口令哈希。
//
// salt 会被截断到 8 位（apr1 的规定）；传空则自动生成。
//
// 返回值形如 "$apr1$abcdefgh$EfExgQSMBXioDhIVk8IOb1"。
func Hash(password, salt string) (string, error) {
	if salt == "" {
		var err error
		salt, err = Salt()
		if err != nil {
			return "", err
		}
	}
	if len(salt) > 8 {
		salt = salt[:8]
	}
	pw := []byte(password)
	s := []byte(salt)

	// ---- 阶段 1：初值与编排 ----
	//
	// ctx 的内容依次是：
	//   口令 + "$apr1$" + 盐
	//   + md5(口令+盐+口令) 的前 min(n,16) 字节（n 从口令长度每次减 16）
	//   + 按口令长度二进制位决定的一串字节（奇数位补 0x00，偶数位补口令首字节）
	ctx := md5.New()
	ctx.Write(pw)
	ctx.Write([]byte(magic))
	ctx.Write(s)

	sum1 := md5.Sum(append(append(append([]byte{}, pw...), s...), pw...))
	for n := len(pw); n > 0; n -= 16 {
		// 注意：n 不能在这里被改写。循环变量每轮要减 16，
		// 一旦把 n 改成 16，n -= 16 之后循环立刻结束——
		// 长度超过 16 字节的口令就会少算一段，结果与 Apache 不一致。
		k := n
		if k > 16 {
			k = 16
		}
		ctx.Write(sum1[:k])
	}

	for i := len(pw); i > 0; i >>= 1 {
		if i&1 != 0 {
			ctx.Write([]byte{0})
		} else {
			ctx.Write(pw[:1])
		}
	}

	sum := ctx.Sum(nil)

	// ---- 阶段 2：1000 次迭代 ----
	//
	// 每轮四项：口令或摘要（按轮次奇偶）+ 盐（i%3）+ 口令（i%7）+ 摘要或口令（按轮次奇偶）
	for i := 0; i < 1000; i++ {
		c := md5.New()
		if i&1 != 0 {
			c.Write(pw)
		} else {
			c.Write(sum)
		}
		if i%3 != 0 {
			c.Write(s)
		}
		if i%7 != 0 {
			c.Write(pw)
		}
		if i&1 != 0 {
			c.Write(sum)
		} else {
			c.Write(pw)
		}
		sum = c.Sum(nil)
	}

	return magic + salt + "$" + encode(sum), nil
}

// encode 把 16 字节摘要编码成 22 个字符。
//
// 字节编排是 crypt(3) 的交错序：
//
//	第 1 组 = b[0],  b[6],  b[12]
//	第 2 组 = b[1],  b[7],  b[13]
//	第 3 组 = b[2],  b[8],  b[14]
//	第 4 组 = b[3],  b[9],  b[15]
//	第 5 组 = b[4],  b[10], b[5]
//	第 6 组 = b[11]
//
// 每组按 6 比特一组、**低位在前**输出（little-endian base64 变体）。
func encode(sum []byte) string {
	var sb strings.Builder
	sb.Grow(22)
	put := func(v uint32, count int) {
		for i := 0; i < count; i++ {
			sb.WriteByte(alphabet[v&0x3f])
			v >>= 6
		}
	}
	put(uint32(sum[0])<<16|uint32(sum[6])<<8|uint32(sum[12]), 4)
	put(uint32(sum[1])<<16|uint32(sum[7])<<8|uint32(sum[13]), 4)
	put(uint32(sum[2])<<16|uint32(sum[8])<<8|uint32(sum[14]), 4)
	put(uint32(sum[3])<<16|uint32(sum[9])<<8|uint32(sum[15]), 4)
	put(uint32(sum[4])<<16|uint32(sum[10])<<8|uint32(sum[5]), 4)
	put(uint32(sum[11]), 2)
	return sb.String()
}

// Verify 校验口令是否与已有的 apr1 哈希匹配。
//
// 用途：面板自身不需要（nginx 直接读文件校验），
// 但运维接口"测试访问认证口令"和回归测试都要用。
func Verify(password, hash string) bool {
	rest, ok := strings.CutPrefix(hash, magic)
	if !ok {
		return false
	}
	salt, digest, ok := strings.Cut(rest, "$")
	if !ok || salt == "" || len(digest) != 22 {
		return false
	}
	got, err := Hash(password, salt)
	if err != nil {
		return false
	}
	// 常量时间比较，避免通过响应时间做逐字节猜测
	if len(got) != len(hash) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ hash[i]
	}
	return diff == 0
}

// Entry 生成 htpasswd 文件里的一行 "user:hash"。
func Entry(user, password string) (string, error) {
	if user == "" {
		return "", fmt.Errorf("用户名不能为空")
	}
	// 用户名里不能有冒号或换行（htpasswd 的分隔符）
	if strings.ContainsAny(user, ":\r\n") {
		return "", fmt.Errorf("用户名 %q 含非法字符", user)
	}
	salt, err := Salt()
	if err != nil {
		return "", err
	}
	h, err := Hash(password, salt)
	if err != nil {
		return "", err
	}
	return user + ":" + h, nil
}

// PlainEntry 生成明文口令条目。
//
// 仅在明确需要与外部工具互通时使用（例如由管理员自己用 htpasswd 命令
// 生成的文件）。面板自身一律用 apr1。
func PlainEntry(user, password string) (string, error) {
	if user == "" {
		return "", fmt.Errorf("用户名不能为空")
	}
	if strings.ContainsAny(user, ":\r\n") {
		return "", fmt.Errorf("用户名 %q 含非法字符", user)
	}
	if strings.ContainsAny(password, "\r\n") {
		return "", fmt.Errorf("口令不能包含换行")
	}
	return user + ":" + password, nil
}
