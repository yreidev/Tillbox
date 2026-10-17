package alipayqr

import (
	"encoding/json"
	"net/url"
	"strings"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/secret"
)

// Input 是后台提交的经营码渠道配置。
type Input struct {
	AppID           string `json:"app_id"`
	AppPrivateKey   string `json:"app_private_key"` // 可选：导入已有的应用私钥；空表示保留原值
	AlipayPublicKey string `json:"alipay_public_key"`
	Gateway         string `json:"gateway"`
	QRCodeURL       string `json:"qrcode_url"`
	PayeeName       string `json:"payee_name"`
}

// View 是给后台看的配置，不含私钥。
type View struct {
	AppID           string `json:"app_id"`
	HasPrivateKey   bool   `json:"has_private_key"`
	AppPublicKey    string `json:"app_public_key"`
	AlipayPublicKey string `json:"alipay_public_key"`
	Gateway         string `json:"gateway"`
	QRCodeURL       string `json:"qrcode_url"`
	PayeeName       string `json:"payee_name"`
}

// ViewOf 把渠道表里的配置转成后台看的样子。
func ViewOf(raw string) View {
	c, _ := ParseConfig(raw)
	return View{
		AppID: c.AppID, HasPrivateKey: c.AppPrivateKey != "", AppPublicKey: c.AppPublicKey,
		AlipayPublicKey: c.AlipayPublicKey, Gateway: c.Gateway, QRCodeURL: c.QRCodeURL, PayeeName: c.PayeeName,
	}
}

func validQRCode(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, "qr.alipay.com") && len(u.Path) > 1
}

// Merge 校验后台提交的配置，合并进当前配置后返回新的配置原文。私钥加密保存，没填时保留原值。
func Merge(box *secret.Box, cur string, in json.RawMessage, enabled bool) (string, error) {
	c, err := ParseConfig(cur)
	if err != nil {
		c = Config{Gateway: GatewayProd}
	}
	var v Input
	if len(in) > 0 {
		if err := json.Unmarshal(in, &v); err != nil {
			return "", channel.Invalid("经营码配置格式不对")
		}
	}
	c.AppID = strings.TrimSpace(v.AppID)
	c.AlipayPublicKey = strings.Join(strings.Fields(v.AlipayPublicKey), "")
	c.QRCodeURL = strings.TrimSpace(v.QRCodeURL)
	c.PayeeName = strings.TrimSpace(v.PayeeName)
	gw := strings.TrimRight(strings.TrimSpace(v.Gateway), "/")
	if gw == "" {
		gw = GatewayProd
	}
	if u, err := url.Parse(gw); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", channel.Invalid("支付宝网关地址不对")
	}
	c.Gateway = gw
	if c.AppID != "" && strings.Trim(c.AppID, "0123456789") != "" {
		return "", channel.Invalid("应用 ID（AppID）应该是一串数字")
	}
	if c.AlipayPublicKey != "" {
		if _, err := epay.ParsePublicKey(c.AlipayPublicKey); err != nil {
			return "", channel.Invalid("支付宝公钥格式不对：请粘贴支付宝开放平台里的「支付宝公钥」，不是应用公钥")
		}
	}
	if c.QRCodeURL != "" && !validQRCode(c.QRCodeURL) {
		return "", channel.Invalid("经营码内容应该是 https://qr.alipay.com/ 开头的链接")
	}
	if len([]rune(c.PayeeName)) > 32 {
		return "", channel.Invalid("收款方名称不超过 32 个字")
	}
	if k := strings.TrimSpace(v.AppPrivateKey); k != "" {
		priv, err := epay.ParsePrivateKey(k)
		if err != nil {
			return "", channel.Invalid("应用私钥格式不对：%v", err)
		}
		pub, err := epay.PublicKeyOf(priv)
		if err != nil {
			return "", err
		}
		plain, err := epay.MarshalPrivateKey(priv)
		if err != nil {
			return "", err
		}
		sealed, err := box.Seal(plain)
		if err != nil {
			return "", err
		}
		c.AppPrivateKey, c.AppPublicKey = sealed, pub
	}
	if enabled && (c.AppID == "" || c.AppPrivateKey == "" || c.AlipayPublicKey == "" || c.QRCodeURL == "") {
		return "", channel.Invalid("启用前要填完整：应用 ID、应用密钥、支付宝公钥、经营码")
	}
	b, err := json.Marshal(c)
	return string(b), err
}

// Keypair 生成新的应用密钥对：私钥加密写进配置，公钥返回给用户上传到支付宝开放平台。
func Keypair(box *secret.Box, cur string) (cfg, pub string, err error) {
	c, err := ParseConfig(cur)
	if err != nil {
		c = Config{Gateway: GatewayProd}
	}
	priv, pub, err := epay.GenerateKeyPair()
	if err != nil {
		return "", "", err
	}
	sealed, err := box.Seal(priv)
	if err != nil {
		return "", "", err
	}
	c.AppPrivateKey, c.AppPublicKey = sealed, pub
	b, err := json.Marshal(c)
	return string(b), pub, err
}
