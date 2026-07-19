package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JWKSCache holds public keys for JWT verification. In integrated mode,
// keys are loaded directly from the JWTIssuer's JWKS JSON output.
type JWKSCache struct {
	keys map[string]*rsa.PublicKey
}

// NewJWKSCacheFromIssuer creates a JWKS cache pre-loaded from a local JWTIssuer.
func NewJWKSCacheFromIssuer(issuer *JWTIssuer) (*JWKSCache, error) {
	jwksJSON, err := issuer.JWKSJSON()
	if err != nil {
		return nil, fmt.Errorf("get JWKS from issuer: %w", err)
	}
	keys, err := parseJWKS(jwksJSON)
	if err != nil {
		return nil, err
	}
	return &JWKSCache{keys: keys}, nil
}

// GetKey returns the public key for the given key ID.
func (c *JWKSCache) GetKey(kid string) (*rsa.PublicKey, error) {
	key, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}
	return key, nil
}

// VerifyToken verifies a JWT token signature using cached JWKS keys.
func (c *JWKSCache) VerifyToken(token string) (*Claims, error) {
	parts := splitJWT(token)
	if parts == nil {
		return nil, fmt.Errorf("malformed JWT")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}

	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	key, err := c.GetKey(header.Kid)
	if err != nil {
		return nil, err
	}

	// Verify signature
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], sig); err != nil {
		return nil, fmt.Errorf("invalid signature: %w", err)
	}

	// Check payload claims: expiration, issuer, audience, and token type.
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var payloadCheck struct {
		Exp  int64  `json:"exp"`
		Iss  string `json:"iss"`
		Aud  string `json:"aud"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payloadJSON, &payloadCheck); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	if time.Now().Unix() > payloadCheck.Exp {
		return nil, fmt.Errorf("token expired")
	}
	if payloadCheck.Iss != TokenIssuer {
		return nil, fmt.Errorf("unexpected token issuer")
	}
	if payloadCheck.Type == "refresh" {
		return nil, fmt.Errorf("refresh token cannot be used for authentication")
	}
	if payloadCheck.Aud != AccessTokenAudience {
		return nil, fmt.Errorf("unexpected token audience")
	}

	return ParseUnverifiedClaims(token)
}

func parseJWKS(jwksJSON string) (map[string]*rsa.PublicKey, error) {
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(jwksJSON), &jwks); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
	}
	return keys, nil
}

func splitJWT(token string) []string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	return parts
}
