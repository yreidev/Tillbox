package epay

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 向量由 scripts/sign-vectors.sh 用官方服务端和官方 SDK 生成。
type vectors struct {
	MD5Key        string `json:"md5_key"`
	RSAPrivateKey string `json:"rsa_private_key"`
	RSAPublicKey  string `json:"rsa_public_key"`
	MD5           []struct {
		Title        string `json:"title"`
		Params       Params `json:"params"`
		Content      string `json:"content"`
		Sign         string `json:"sign"`
		ServerVerify bool   `json:"server_verify"`
		SDKV1Sign    string `json:"sdk_v1_sign"`
	} `json:"md5"`
	RSA []struct {
		Title        string `json:"title"`
		Params       Params `json:"params"`
		Content      string `json:"content"`
		Sign         string `json:"sign"`
		ServerVerify bool   `json:"server_verify"`
		SDKV2Content string `json:"sdk_v2_content"`
		SDKV2Sign    string `json:"sdk_v2_sign"`
	} `json:"rsa"`
	Values []struct {
		Title   string `json:"title"`
		JSON    string `json:"json"`
		Content string `json:"content"`
		Sign    string `json:"sign"`
	} `json:"values"`
}

func loadVectors(t *testing.T) *vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/sign_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.MD5) == 0 || len(v.RSA) == 0 || len(v.Values) == 0 {
		t.Fatal("向量文件不完整")
	}
	return &v
}

func TestMD5Vectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.MD5 {
		t.Run(c.Title, func(t *testing.T) {
			if !c.ServerVerify {
				t.Fatal("官方服务端没有验过自己的签名，向量有问题")
			}
			if got := SignContent(c.Params); got != c.Content {
				t.Errorf("待签名串\n got %q\nwant %q", got, c.Content)
			}
			if got := MD5Sign(c.Params, v.MD5Key); got != c.Sign {
				t.Errorf("签名 got %s want %s", got, c.Sign)
			}
			p := clone(c.Params)
			p["sign"] = c.Sign
			if !MD5Verify(p, v.MD5Key) {
				t.Error("验签失败")
			}
			p["sign"] = strings.ToUpper(c.Sign)
			if MD5Verify(p, v.MD5Key) {
				t.Error("大写签名不该通过（官方用 === 比较）")
			}
			// 官方 V1 SDK 只跳过空串，带纯空白参数时和服务端算出来的不一样；其他情况必须一致。
			hasBlank := false
			for k, val := range c.Params {
				if k != "sign" && k != "sign_type" && val != "" && IsEmpty(val) {
					hasBlank = true
				}
			}
			if !hasBlank && c.SDKV1Sign != c.Sign {
				t.Errorf("官方 V1 SDK 和服务端的签名不一致：%s / %s", c.SDKV1Sign, c.Sign)
			}
		})
	}
}

func TestRSAVectors(t *testing.T) {
	v := loadVectors(t)
	priv, err := ParsePrivateKey(v.RSAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublicKey(v.RSAPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.RSA {
		t.Run(c.Title, func(t *testing.T) {
			if !c.ServerVerify || c.Content != c.SDKV2Content || c.Sign != c.SDKV2Sign {
				t.Fatal("官方服务端和 SDK 自己就不一致，向量有问题")
			}
			if got := SignContent(c.Params); got != c.Content {
				t.Errorf("待签名串\n got %q\nwant %q", got, c.Content)
			}
			// PKCS#1 v1.5 签名是确定的，同一私钥算出来必须逐字节相同。
			got, err := RSASign(c.Content, priv)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Sign {
				t.Errorf("签名和官方不同")
			}
			if !RSAVerify(c.Content, c.Sign, pub) {
				t.Error("验不过官方的签名")
			}
			if RSAVerify(c.Content+"x", c.Sign, pub) {
				t.Error("改了内容还能验过")
			}
		})
	}
}

func TestSignValuesVectors(t *testing.T) {
	v := loadVectors(t)
	priv, err := ParsePrivateKey(v.RSAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Values {
		t.Run(c.Title, func(t *testing.T) {
			d := json.NewDecoder(bytes.NewReader([]byte(c.JSON)))
			d.UseNumber()
			var raw map[string]any
			if err := d.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			m := map[string]any{}
			for k, x := range raw {
				if n, ok := x.(json.Number); ok {
					i, err := n.Int64()
					if err != nil {
						t.Fatal(err)
					}
					x = i
				}
				m[k] = x
			}
			if got := SignContent(SignValues(m)); got != c.Content {
				t.Errorf("待签名串\n got %q\nwant %q", got, c.Content)
			}
			if got, _ := RSASign(c.Content, priv); got != c.Sign {
				t.Error("签名和官方不同")
			}
		})
	}
}

func clone(p Params) Params {
	q := Params{}
	for k, v := range p {
		q[k] = v
	}
	return q
}

func TestKeyFormats(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	// 纯 Base64、带换行的 Base64、完整 PEM 都要认。
	wrapped := toPEM(pub, "PUBLIC KEY")
	for _, s := range []string{pub, " " + pub + "\n", wrapped, strings.ReplaceAll(wrapped, "\n", "\r\n")} {
		if _, err := ParsePublicKey(s); err != nil {
			t.Errorf("没认出公钥 %q：%v", s[:20], err)
		}
	}
	if got, _ := PublicKeyOf(k); got != pub {
		t.Error("从私钥推出的公钥不对")
	}
	again, _ := MarshalPrivateKey(k)
	if again != priv {
		t.Error("私钥导出后变了")
	}
	if _, err := ParsePublicKey("abc"); err == nil {
		t.Error("乱码也当成公钥了")
	}
	if _, err := ParsePrivateKey(pub); err == nil {
		t.Error("公钥被当成私钥了")
	}
}

func TestIsEmpty(t *testing.T) {
	for v, want := range map[string]bool{"": true, " ": true, "\t\r\n": true, "\x00": true, "\x0B": true, "0": false, " a ": false, "　": false} {
		if IsEmpty(v) != want {
			t.Errorf("IsEmpty(%q) 应为 %v", v, want)
		}
	}
	for v, want := range map[string]bool{"": true, "0": true, "00": false, " ": false, "0.0": false} {
		if PHPEmpty(v) != want {
			t.Errorf("PHPEmpty(%q) 应为 %v", v, want)
		}
	}
}
