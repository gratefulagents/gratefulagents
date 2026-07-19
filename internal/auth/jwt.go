package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

// JWTIssuer signs access and refresh tokens using RS256.
type JWTIssuer struct {
	privateKey *rsa.PrivateKey
	keyID      string
}

const (
	// TokenIssuer is the iss claim stamped on and required from all tokens.
	TokenIssuer = "gratefulagents-auth"
	// AccessTokenAudience is the aud claim required on access tokens.
	AccessTokenAudience = "gratefulagents-dashboard"
)

// NewJWTIssuer creates an issuer. If keyPath is empty, generates an ephemeral key pair.
func NewJWTIssuer(keyPath string) (*JWTIssuer, error) {
	var pk *rsa.PrivateKey
	var err error

	if keyPath != "" {
		pk, err = loadPrivateKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("load private key: %w", err)
		}
	} else {
		pk, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
	}

	return newJWTIssuerFromKey(pk)
}

func newJWTIssuerFromKey(pk *rsa.PrivateKey) (*JWTIssuer, error) {
	pubDER, err := x509.MarshalPKIXPublicKey(&pk.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	hash := sha256.Sum256(pubDER)
	kid := base64.RawURLEncoding.EncodeToString(hash[:8])

	return &JWTIssuer{privateKey: pk, keyID: kid}, nil
}

// AccessTokenClaims are the claims embedded in an access JWT.
type AccessTokenClaims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name"`
	Picture  string `json:"picture,omitempty"`
	Role     string `json:"role"`
}

// IssueAccessToken creates a signed JWT access token valid for the given duration.
func (j *JWTIssuer) IssueAccessToken(claims AccessTokenClaims, ttl time.Duration) (string, int64, error) {
	now := time.Now()
	exp := now.Add(ttl)

	header := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": j.keyID,
	}

	payload := map[string]interface{}{
		"sub":      claims.Sub,
		"username": claims.Username,
		"email":    claims.Email,
		"name":     claims.Name,
		"picture":  claims.Picture,
		"role":     claims.Role,
		"iat":      now.Unix(),
		"exp":      exp.Unix(),
		"iss":      TokenIssuer,
		"aud":      AccessTokenAudience,
	}

	token, err := signJWT(header, payload, j.privateKey)
	if err != nil {
		return "", 0, err
	}
	return token, exp.Unix(), nil
}

// IssueRefreshToken creates a signed JWT refresh token valid for the given duration.
func (j *JWTIssuer) IssueRefreshToken(userID string, ttl time.Duration) (string, int64, error) {
	now := time.Now()
	exp := now.Add(ttl)

	header := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": j.keyID,
	}

	payload := map[string]interface{}{
		"sub":  userID,
		"type": "refresh",
		"iat":  now.Unix(),
		"exp":  exp.Unix(),
		"iss":  TokenIssuer,
	}

	token, err := signJWT(header, payload, j.privateKey)
	if err != nil {
		return "", 0, err
	}
	return token, exp.Unix(), nil
}

// JWKSJSON returns the JSON-encoded JWKS containing the public key.
func (j *JWTIssuer) JWKSJSON() (string, error) {
	pub := &j.privateKey.PublicKey

	jwks := map[string]interface{}{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": j.keyID,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}

	data, err := json.Marshal(jwks)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func signJWT(header map[string]string, payload map[string]interface{}, key *rsa.PrivateKey) (string, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := headerB64 + "." + payloadB64

	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parsePrivateKeyPEM(data)
}

func parsePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA")
	}
	return rsaKey, nil
}
