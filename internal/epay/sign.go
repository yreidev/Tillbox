// Package epay 实现易支付协议（V1 MD5 + V2 RSA），行为按官方服务端和官方 SDK 对齐。
// 规格见 docs/research/epay-protocol.md。
package epay

import (
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Params 是一组请求或通知参数。值都已经是 URL 解码后的原文。
type Params map[string]string

// phpTrimSet 是 PHP trim() 默认去掉的字符。
const phpTrimSet = " \t\n\r\x00\x0B"

// IsEmpty 对应官方的 isEmpty：去掉首尾空白后为空串即为空。"0" 不算空。
func IsEmpty(v string) bool {
	return strings.Trim(v, phpTrimSet) == ""
}

// PHPEmpty 对应 PHP 的 empty()（只看字符串）：空串和 "0" 都算空。
func PHPEmpty(v string) bool { return v == "" || v == "0" }

// SignContent 生成待签名串：去掉 sign、sign_type 和空值，参数名按字节序排序，拼成 k=v&k=v，不做 URL 编码。
func SignContent(p Params) string {
	keys := make([]string, 0, len(p))
	for k, v := range p {
		if k == "sign" || k == "sign_type" || IsEmpty(v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(p[k])
	}
	return b.String()
}

// MD5Sign 计算 MD5 签名：md5(待签名串 + key)，小写十六进制。
func MD5Sign(p Params, key string) string {
	sum := md5.Sum([]byte(SignContent(p) + key))
	return hex.EncodeToString(sum[:])
}

// MD5Verify 校验 MD5 签名，区分大小写（官方用 ===）。
func MD5Verify(p Params, key string) bool {
	want := MD5Sign(p, key)
	return subtle.ConstantTimeCompare([]byte(want), []byte(p["sign"])) == 1
}

// RSASign 用 SHA256WithRSA（PKCS#1 v1.5）签名，结果为标准 Base64。
func RSASign(content string, priv *rsa.PrivateKey) (string, error) {
	h := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// RSAVerify 校验 SHA256WithRSA 签名。签名是标准 Base64。
func RSAVerify(content, sign string, pub *rsa.PublicKey) bool {
	sig, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return false
	}
	h := sha256.Sum256([]byte(content))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig) == nil
}

// toPEM 对应官方 base64ToPem：已经带 -----BEGIN 的原样返回；否则每 64 个字符换行并补上 PEM 头尾。
func toPEM(data, typ string) string {
	data = strings.TrimSpace(data)
	if data == "" || strings.Contains(data, "-----BEGIN") {
		return data
	}
	compact := strings.Join(strings.Fields(data), "")
	var b strings.Builder
	b.WriteString("-----BEGIN " + typ + "-----\n")
	for i := 0; i < len(compact); i += 64 {
		end := min(i+64, len(compact))
		b.WriteString(compact[i:end])
		b.WriteByte('\n')
	}
	b.WriteString("-----END " + typ + "-----")
	return b.String()
}

// ParsePublicKey 解析 RSA 公钥。接受纯 Base64（SubjectPublicKeyInfo）或完整 PEM（PUBLIC KEY / RSA PUBLIC KEY）。
func ParsePublicKey(s string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(toPEM(s, "PUBLIC KEY")))
	if block == nil {
		return nil, errors.New("公钥格式不正确")
	}
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("不是 RSA 公钥")
	}
	if k, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("公钥格式不正确")
}

// ParsePrivateKey 解析 RSA 私钥。接受纯 Base64（PKCS#8）或完整 PEM（PKCS#8 / PKCS#1）。
func ParsePrivateKey(s string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(toPEM(s, "PRIVATE KEY")))
	if block == nil {
		return nil, errors.New("私钥格式不正确")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("不是 RSA 私钥")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("私钥格式不正确")
}

// GenerateKeyPair 生成 2048 位 RSA 密钥对，返回纯 Base64 的 PKCS#8 私钥和 SubjectPublicKeyInfo 公钥（官方交换格式）。
func GenerateKeyPair() (priv, pub string, err error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(privDER), base64.StdEncoding.EncodeToString(pubDER), nil
}

// PublicKeyOf 从私钥推出公钥（纯 Base64）。
func PublicKeyOf(priv *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// SignValues 把 V2 响应里的值转成官方 SDK 解码后拼接时用的字符串：字符串原样，整数十进制，其余类型不参与签名。
// 本系统约定签名响应里只放字符串和整数。
func SignValues(m map[string]any) Params {
	p := Params{}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			p[k] = x
		case int:
			p[k] = strconv.Itoa(x)
		case int64:
			p[k] = strconv.FormatInt(x, 10)
		}
	}
	return p
}

// DecodedValues 把 JSON 响应（json.Decoder 开了 UseNumber）转成官方 SDK 拼待签名串时用的字符串，用来验别家平台的响应：
// 字符串原样，整数十进制，浮点数按 PHP 默认精度（14 位有效数字）转换，true 为 "1"，false、null、数组和对象跳过。
func DecodedValues(m map[string]any) Params {
	p := Params{}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			p[k] = x
		case json.Number:
			if _, err := strconv.ParseInt(string(x), 10, 64); err == nil {
				p[k] = string(x)
			} else if f, err := x.Float64(); err == nil {
				p[k] = strconv.FormatFloat(f, 'G', 14, 64)
			}
		case float64:
			p[k] = strconv.FormatFloat(x, 'G', 14, 64)
		case bool:
			if x {
				p[k] = "1"
			}
		}
	}
	return p
}

// MarshalPrivateKey 把私钥转成纯 Base64 的 PKCS#8（官方交换格式）。
func MarshalPrivateKey(priv *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}
