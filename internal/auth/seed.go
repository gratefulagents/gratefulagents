package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	adminSecretName = "gratefulagents-admin-credentials"
	adminUsername   = "admin"
	passwordLength  = 24
)

// SeedAdmin ensures a local admin user exists. It reads or creates a Kubernetes
// secret containing the admin credentials, then upserts the admin user in the
// auth store.
func SeedAdmin(ctx context.Context, clientset kubernetes.Interface, store Store) error {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	secrets := clientset.CoreV1().Secrets(namespace)

	var password string

	existing, err := secrets.Get(ctx, adminSecretName, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("checking admin secret: %w", err)
		}
		// Secret doesn't exist — generate new password and create it.
		password, err = generatePassword(passwordLength)
		if err != nil {
			return fmt.Errorf("generating admin password: %w", err)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adminSecretName,
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"username": []byte(adminUsername),
				"password": []byte(password),
			},
		}
		if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating admin secret: %w", err)
		}
		log.Printf("Admin credentials created — retrieve with: kubectl get secret %s -n %s -o jsonpath='{.data.password}' | base64 -d",
			adminSecretName, namespace)
	} else {
		password = string(existing.Data["password"])
		if password == "" {
			return fmt.Errorf("admin secret exists but has no password")
		}
	}

	// If the admin user already exists, leave it untouched: re-seeding would
	// overwrite a manually adjusted role and needlessly re-hash the password.
	if existingUser, err := store.GetUserByUsername(ctx, adminUsername); err == nil && existingUser != nil {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing admin password: %w", err)
	}

	_, err = store.UpsertUser(ctx, &User{
		Username:     adminUsername,
		Name:         "Admin",
		PasswordHash: string(hash),
		Role:         RoleAdmin,
	})
	if err != nil {
		return fmt.Errorf("seeding admin user: %w", err)
	}

	log.Printf("Admin user seeded (username: %s)", adminUsername)
	return nil
}

func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n], nil
}
