// Package secret 用主密钥加密存库的敏感字段（商户密钥、支付宝应用私钥、平台私钥等）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// 密文前缀，便于以后换算法时区分版本。
const prefix = "v1:"

type Box struct {
	aead cipher.AEAD
}

// New 用 32 字节主密钥创建加解密器。
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("主密钥必须是 32 字节，实际 %d 字节", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadKey 按顺序取主密钥：环境变量给的 Base64 → dataDir/master.key → 新生成一个写进 dataDir/master.key。
func LoadKey(fromEnv, dataDir string) ([]byte, error) {
	if fromEnv != "" {
		return decodeKey(fromEnv, "TILLBOX_MASTER_KEY")
	}
	path := filepath.Join(dataDir, "master.key")
	b, err := os.ReadFile(path)
	if err == nil {
		return decodeKey(strings.TrimSpace(string(b)), path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, err
	}
	// 确认主密钥落盘后才能用它加密，否则重启后库里的密钥都解不开
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	slog.Warn("已生成新的主密钥，丢失它就无法解密库里的密钥，请备份", "path", path)
	return key, nil
}

func decodeKey(s, from string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s 不是合法的 Base64：%w", from, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s 解码后必须是 32 字节，实际 %d 字节", from, len(key))
	}
	return key, nil
}

// Seal 加密。空串原样返回，表示"没有设置"。
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open 解密。空串原样返回。
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, prefix) {
		return "", errors.New("密文格式不认识")
	}
	raw, err := base64.StdEncoding.DecodeString(sealed[len(prefix):])
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("密文太短")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("解密失败，主密钥可能不对")
	}
	return string(plain), nil
}
