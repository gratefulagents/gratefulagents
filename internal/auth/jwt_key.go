package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	jwtKeySecretName = "gratefulagents-jwt-signing-key"
	jwtKeySecretKey  = "private.pem"
)

// EnsureSigningKeyPEM returns a persistent PEM-encoded RSA signing key backed
// by a Kubernetes Secret, generating and storing one on first use. This keeps
// JWTs valid across manager restarts and consistent across replicas, unlike
// the previous ephemeral in-memory key.
func EnsureSigningKeyPEM(ctx context.Context, clientset kubernetes.Interface) ([]byte, error) {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	secrets := clientset.CoreV1().Secrets(namespace)

	existing, err := secrets.Get(ctx, jwtKeySecretName, metav1.GetOptions{})
	if err == nil {
		if pemBytes := existing.Data[jwtKeySecretKey]; len(pemBytes) > 0 {
			return pemBytes, nil
		}
		return nil, fmt.Errorf("secret %s/%s exists but has no %q key", namespace, jwtKeySecretName, jwtKeySecretKey)
	}
	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("checking JWT signing key secret: %w", err)
	}

	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating JWT signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	if err != nil {
		return nil, fmt.Errorf("marshaling JWT signing key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jwtKeySecretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{jwtKeySecretKey: pemBytes},
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		// Another replica may have created it concurrently — re-read and use it.
		if k8serrors.IsAlreadyExists(err) {
			created, getErr := secrets.Get(ctx, jwtKeySecretName, metav1.GetOptions{})
			if getErr != nil {
				return nil, fmt.Errorf("reading concurrently created JWT signing key secret: %w", getErr)
			}
			if pemBytes := created.Data[jwtKeySecretKey]; len(pemBytes) > 0 {
				return pemBytes, nil
			}
			return nil, fmt.Errorf("concurrently created secret %s/%s has no %q key", namespace, jwtKeySecretName, jwtKeySecretKey)
		}
		return nil, fmt.Errorf("creating JWT signing key secret: %w", err)
	}
	log.Printf("JWT signing key generated and persisted in secret %s/%s", namespace, jwtKeySecretName)
	return pemBytes, nil
}

// NewJWTIssuerFromPEM creates an issuer from a PEM-encoded RSA private key.
func NewJWTIssuerFromPEM(pemBytes []byte) (*JWTIssuer, error) {
	pk, err := parsePrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return newJWTIssuerFromKey(pk)
}
