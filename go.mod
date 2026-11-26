module github.com/zhiwei/panel

// go 1.26.10：修复 GO-2026-6809（crypto/x509 信任已被攻陷的中间 CA）。
// go1.26.9 受影响，本项目的 internal/logmgr 在做证书指纹校验时会走到
// x509.Certificate.Verify，属于可达路径，因此必须升到 1.26.10 及以上。
go 1.26.10

require (
	github.com/go-acme/lego/v4 v4.35.2
	github.com/go-sql-driver/mysql v1.10.2
	github.com/gorilla/websocket v1.5.3
	github.com/robfig/cron/v3 v3.0.1
	golang.org/x/crypto v0.58.0
	modernc.org/sqlite v1.63.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/alibabacloud-go/alibabacloud-gateway-spi v0.0.5 // indirect
	github.com/alibabacloud-go/darabonba-openapi/v2 v2.1.16 // indirect
	github.com/alibabacloud-go/debug v1.0.1 // indirect
	github.com/alibabacloud-go/tea v1.4.0 // indirect
	github.com/alibabacloud-go/tea-utils/v2 v2.0.9 // indirect
	github.com/aliyun/credentials-go v1.4.7 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/clbanning/mxj/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-acme/alidns-20150109/v4 v4.7.0 // indirect
	github.com/go-acme/tencentclouddnspod v1.3.24 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.13-0.20220915233716-71ac16282d12 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/nrdcg/dnspod-go v0.4.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common v1.3.83 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	gopkg.in/ini.v1 v1.67.1 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
