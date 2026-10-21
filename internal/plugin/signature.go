// Package plugin — M20-02 插件签名校验（SNAPSHOT）
//
// 本文件实现插件签名校验机制，对标 sigstore/cosign 的"签名+公钥"模式。
// 简化版：使用 ed25519 数字签名，签名文件 (plugin.json.sig) 与 manifest 同目录存放。
//
// 对标：
// - sigstore cosign：https://docs.sigstore.dev/cosign/signing/overview/
// - VSCode extension publisher signature：https://code.visualstudio.com/api/working-with-extensions/publishing-extension
//
// 设计要点（候选 A 选定）：
// - 接口签名：SignatureVerifier 接口抽象，支持多算法
// - 结构体字段：SignatureConfig 含 PublicKey/Required/Algorithm
// - 分层权责：本文件只做"验签"，拉取由 marketplace，执行由 sandbox
// - 降级策略：Required=false 时无签名允许安装（记录警告）；Required=true 时拒绝
// - TTL规则：公钥指纹缓存 1 小时（避免重复解析）
//
// 安全约束：
// - CGO_ENABLED=0：仅用 Go 标准库 crypto/ed25519
// - 不引入 cosign CLI 依赖（避免外部工具依赖）
// - 签名格式：base64 标准编码（cosign 用 base64，此处对齐）
package plugin

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sigKeyCacheTTL 是公钥解析缓存有效期（1 小时）。
const sigKeyCacheTTL = 1 * time.Hour

// SignatureAlgorithm 是签名算法标识。
type SignatureAlgorithm string

const (
	// SigAlgEd25519 是 ed25519 算法（默认，推荐）。
	SigAlgEd25519 SignatureAlgorithm = "ed25519"
	// SigAlgNone 表示无签名（仅用于降级场景）。
	SigAlgNone SignatureAlgorithm = "none"
)

// SignatureConfig 是签名校验配置（SNAPSHOT-1）。
//
// 字段说明：
// - PublicKey：ed25519 公钥（32 字节，base64 编码的字符串）
// - Required：true 时无签名拒绝安装；false 时允许无签名（记录警告）
// - Algorithm：签名算法（默认 ed25519）
type SignatureConfig struct {
	PublicKey string             `json:"public_key"` // base64 编码的公钥
	Required  bool               `json:"required"`   // 是否强制要求签名
	Algorithm SignatureAlgorithm `json:"algorithm"`  // 算法（默认 ed25519）
}

// SignatureVerifier 是插件签名校验接口（SNAPSHOT-2）。
//
// 接口契约：
// - Verify：校验插件目录下的 manifest 签名
// - VerifyBytes：校验任意字节的签名（供 marketplace 拉取后校验）
//
// 实现方：
// - ed25519Verifier：ed25519 算法（本文件实现）
// - 未来可扩展 cosignVerifier（调 cosign CLI）
type SignatureVerifier interface {
	// Verify 校验插件目录下的 plugin.json 签名。
	// pluginDir 是插件根目录（含 plugin.json + plugin.json.sig）。
	// 返回 nil 表示验签通过；返回 error 表示验签失败或签名缺失。
	Verify(pluginDir string) error

	// VerifyBytes 校验字节数据的签名。
	// data 是原始数据（如 plugin.json 内容）。
	// sig 是 base64 编码的签名。
	VerifyBytes(data []byte, sig string) error
}

// ed25519Verifier 是 ed25519 签名校验实现（候选 A）。
//
// 验签流程：
// 1. 读取 plugin.json + plugin.json.sig
// 2. SHA-512 哈希 plugin.json 内容
// 3. ed25519.Verify(pubkey, hash, sig)
//
// 缓存策略：
// - 公钥解析结果缓存 1 小时（避免每次安装都 base64 decode）
type ed25519Verifier struct {
	cfg      SignatureConfig
	mu       sync.Mutex
	pubKey   ed25519.PublicKey // 解析后的公钥（缓存）
	parsedAt time.Time         // 公钥解析时间
}

// NewSignatureVerifier 创建签名校验器（SNAPSHOT-3）。
//
// 参数：
// - cfg: 签名配置（PublicKey 为空且 Required=false 时返回 noopVerifier）
//
// 返回的实例线程安全（内部 mutex 保护公钥缓存）。
func NewSignatureVerifier(cfg SignatureConfig) SignatureVerifier {
	if cfg.PublicKey == "" && !cfg.Required {
		return &noopVerifier{}
	}
	if cfg.Algorithm == "" {
		cfg.Algorithm = SigAlgEd25519
	}
	return &ed25519Verifier{cfg: cfg}
}

// parsePublicKey 解析 base64 编码的 ed25519 公钥（带缓存， SNAPSHOT-4）。
//
// 缓存策略：
// - 公钥解析结果在 sigKeyCacheTTL 内缓存
// - 超时后重新解析（支持公钥轮换）
func (v *ed25519Verifier) parsePublicKey() (ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// 缓存有效直接返回
	if v.pubKey != nil && time.Since(v.parsedAt) < sigKeyCacheTTL {
		return v.pubKey, nil
	}

	if v.cfg.PublicKey == "" {
		return nil, errors.New("signature: public key not configured")
	}

	keyBytes, err := base64.StdEncoding.DecodeString(v.cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("signature: decode public key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("signature: invalid public key size: got %d, want %d", len(keyBytes), ed25519.PublicKeySize)
	}

	v.pubKey = ed25519.PublicKey(keyBytes)
	v.parsedAt = time.Now()
	return v.pubKey, nil
}

// Verify 校验插件目录下的 plugin.json 签名（SNAPSHOT-5）。
//
// 行为：
// - 读取 pluginDir/plugin.json + pluginDir/plugin.json.sig
// - .sig 文件不存在时：Required=true 返回 error；Required=false 返回 nil（降级）
// - .sig 存在时调用 VerifyBytes 校验
func (v *ed25519Verifier) Verify(pluginDir string) error {
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	sigPath := filepath.Join(pluginDir, "plugin.json.sig")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("signature: read manifest: %w", err)
	}

	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		if os.IsNotExist(err) {
			if v.cfg.Required {
				return fmt.Errorf("signature: signature file missing and Required=true")
			}
			// 降级：无签名允许（记录警告由调用方处理）
			return nil
		}
		return fmt.Errorf("signature: read sig file: %w", err)
	}

	return v.VerifyBytes(data, strings.TrimSpace(string(sigBytes)))
}

// VerifyBytes 校验字节数据的签名（SNAPSHOT-6）。
//
// 算法：
// 1. base64 解码签名
// 2. ed25519.Verify(pubkey, data, sig)（ed25519 内部已做 SHA-512）
//
// 注意：ed25519 标准库内部已对消息做 SHA-512，无需手动哈希。
func (v *ed25519Verifier) VerifyBytes(data []byte, sigB64 string) error {
	pubKey, err := v.parsePublicKey()
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature: decode signature base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature: invalid signature size: got %d, want %d", len(sig), ed25519.SignatureSize)
	}

	if !ed25519.Verify(pubKey, data, sig) {
		return errors.New("signature: ed25519 verification failed")
	}
	return nil
}

// noopVerifier 是空校验器（降级场景， SNAPSHOT-7）。
//
// 当 PublicKey 为空且 Required=false 时使用：
// - Verify/VerifyBytes 总是返回 nil（允许无签名安装）
type noopVerifier struct{}

// Verify 总是返回 nil（降级：无签名校验）。
func (noopVerifier) Verify(_ string) error { return nil }

// VerifyBytes 总是返回 nil（降级：无签名校验）。
func (noopVerifier) VerifyBytes(_ []byte, _ string) error { return nil }

// SignManifest 用 ed25519 私钥对 manifest 签名（导出供插件开发者使用）。
//
// 这是一个工具函数，不在验签路径上，供插件开发者生成签名。
// 返回 base64 编码的签名。
func SignManifest(data []byte, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("signature: invalid private key size: got %d, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	sig := ed25519.Sign(privateKey, data)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// HashManifest 计算 manifest 的 SHA-512 哈希（导出供审计与调试使用）。
//
// 用于生成 manifest 指纹（fingerprint），方便对比 manifest 是否被篡改。
// 注意：ed25519 验签不需要手动哈希，此函数仅用于审计场景。
func HashManifest(data []byte) string {
	h := sha512.Sum512(data)
	return base64.StdEncoding.EncodeToString(h[:])
}

// ParseSignatureConfig 从 JSON 字节切片解析签名配置（导出供测试与外部调用）。
func ParseSignatureConfig(data []byte) (*SignatureConfig, error) {
	var cfg SignatureConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse signature config: %w", err)
	}
	if cfg.Algorithm == "" {
		cfg.Algorithm = SigAlgEd25519
	}
	return &cfg, nil
}
