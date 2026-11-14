package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// FileHash 文件指纹（存储层）。
type FileHash struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// fileHash 是内部别名，保持既有签名不变。
type fileHash = FileHash

// Hash 返回文件指纹的字符串表示。
func (f fileHash) String() string { return f.Hash }

// TamperEvent 篡改事件。
type TamperEvent struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	Path      string    `json:"path"`
	OldHash   string    `json:"old_hash"`
	NewHash   string    `json:"new_hash"`
	Kind      string    `json:"kind"` // modified / added / deleted
	CreatedAt time.Time `json:"created_at"`
}

// newID 生成随机 ID。
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}

// NewID 导出的 ID 生成函数，供其他包复用。
func NewID() string { return newID() }
