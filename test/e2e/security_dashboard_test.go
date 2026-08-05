//go:build e2e
// +build e2e

/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/crypto/bcrypt"

	authpb "github.com/gratefulagents/gratefulagents/rpc/auth"
	"github.com/gratefulagents/gratefulagents/rpc/auth/authconnect"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"github.com/gratefulagents/gratefulagents/rpc/platform/platformconnect"
	"github.com/gratefulagents/gratefulagents/test/utils"
)

const (
	// dashboardLocalPort is the local end of the kubectl port-forward to the
	// manager's dashboard listener (:8090).
	dashboardLocalPort = "18090"
	dashboardBaseURL   = "http://127.0.0.1:" + dashboardLocalPort

	// e2ePostgresManifest deploys a throwaway Postgres for the dashboard's
	// state/auth store. The pod satisfies the namespace's restricted
	// pod-security policy.
	e2ePostgresManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-postgres
  namespace: gratefulagents-system
  labels:
    app: e2e-postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app: e2e-postgres
  template:
    metadata:
      labels:
        app: e2e-postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - name: POSTGRES_PASSWORD
              value: e2e-postgres-secret
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          ports:
            - containerPort: 5432
          readinessProbe:
            exec:
              command: ["pg_isready", "-U", "postgres"]
            initialDelaySeconds: 2
            periodSeconds: 2
          securityContext:
            runAsNonRoot: true
            runAsUser: 70
            runAsGroup: 70
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
            - name: run
              mountPath: /var/run/postgresql
      volumes:
        - name: data
          emptyDir: {}
        - name: run
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: e2e-postgres
  namespace: gratefulagents-system
spec:
  selector:
    app: e2e-postgres
  ports:
    - port: 5432
      targetPort: 5432
`

	e2ePostgresDSN = "postgres://postgres:e2e-postgres-secret@e2e-postgres." +
		namespace + ".svc.cluster.local:5432/postgres?sslmode=disable"
)

// SecurityScan dashboard smoke test: exercises the deployed manager's
// ConnectRPC dashboard end to end for the SecurityScan surface. It fails when
// any layer drifts: CRD installation (create/get fail), scheme wiring (the
// dashboard cannot list/serve the kind), dashboard RPC authorization (the
// member-user denial stops failing or admin calls start failing), or manager
// RBAC verbs (the controller cannot create AgentRuns / update status, so no
// run appears and the phase never settles).
var _ = Describe("SecurityScan dashboard", Ordered, func() {
	var (
		portForward  *exec.Cmd
		adminClient  platformconnect.PlatformServiceClient
		scanNS       string
		scanName     = "e2e-dashboard-scan"
		memberClient platformconnect.PlatformServiceClient
	)

	ctx := context.Background()

	BeforeAll(func() {
		By("creating the manager namespace")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "create", "ns", namespace)
			out, err := utils.Run(cmd)
			if err != nil && strings.Contains(out, "AlreadyExists") {
				return nil
			}
			return err
		}, 3*time.Minute, 5*time.Second).Should(Succeed(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd := exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("deploying Postgres for the dashboard state store")
		manifest := filepath.Join(os.TempDir(), "e2e-postgres.yaml")
		Expect(os.WriteFile(manifest, []byte(e2ePostgresManifest), 0o644)).To(Succeed())
		cmd = exec.Command("kubectl", "apply", "-f", manifest)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy Postgres")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/e2e-postgres",
			"-n", namespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Postgres did not become ready")

		By("pointing the manager at Postgres")
		cmd = exec.Command("kubectl", "set", "env",
			"deployment/gratefulagents-controller-manager", "-n", namespace,
			"DATABASE_URL="+e2ePostgresDSN)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to set DATABASE_URL")
		cmd = exec.Command("kubectl", "rollout", "status",
			"deployment/gratefulagents-controller-manager", "-n", namespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Manager did not roll out with DATABASE_URL")

		By("waiting for the seeded admin credentials")
		var adminPassword string
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secret", "gratefulagents-admin-credentials",
				"-n", namespace, "-o", "jsonpath={.data.password}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(decoded).NotTo(BeEmpty())
			adminPassword = string(decoded)
		}, 3*time.Minute, 5*time.Second).Should(Succeed(), "Admin credentials were not seeded")

		By("port-forwarding the dashboard")
		portForward = exec.Command("kubectl", "port-forward", "-n", namespace,
			"deployment/gratefulagents-controller-manager", dashboardLocalPort+":8090")
		Expect(portForward.Start()).To(Succeed(), "Failed to start port-forward")
		Eventually(func() error {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+dashboardLocalPort, time.Second)
			if err != nil {
				return err
			}
			return conn.Close()
		}, 2*time.Minute, 2*time.Second).Should(Succeed(), "Dashboard port never became reachable")

		By("logging in as the seeded admin")
		var adminToken string
		Eventually(func(g Gomega) {
			token, err := dashboardLogin(ctx, "admin", adminPassword)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(token).NotTo(BeEmpty())
			adminToken = token
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Admin login failed")
		adminClient = newDashboardClient(adminToken)
	})

	AfterAll(func() {
		if portForward != nil && portForward.Process != nil {
			_ = portForward.Process.Kill()
			_, _ = portForward.Process.Wait()
		}

		By("removing the Postgres deployment")
		cmd := exec.Command("kubectl", "delete", "deployment,service", "e2e-postgres",
			"-n", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	It("creates a SecurityScan configuration through the dashboard", func() {
		resp, err := adminClient.CreateSecurityScan(ctx, connect.NewRequest(&platform.CreateSecurityScanRequest{
			Name: scanName,
			Spec: e2eSecurityScanSpec(false),
		}))
		Expect(err).NotTo(HaveOccurred(), "CreateSecurityScan failed")
		Expect(resp.Msg.GetName()).To(Equal(scanName))
		scanNS = resp.Msg.GetNamespace()
		Expect(scanNS).NotTo(BeEmpty())

		By("verifying the SecurityScan resource exists in the cluster (CRD installed, RBAC create allowed)")
		cmd := exec.Command("kubectl", "get", "securityscans.triggers.gratefulagents.dev",
			scanName, "-n", scanNS)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "SecurityScan CR was not created")

		By("reading the configuration back through the dashboard")
		got, err := adminClient.GetSecurityScanConfig(ctx, connect.NewRequest(&platform.GetSecurityScanConfigRequest{
			Namespace: scanNS, Name: scanName,
		}))
		Expect(err).NotTo(HaveOccurred(), "GetSecurityScanConfig failed")
		Expect(got.Msg.GetSpec().GetRepoUrl()).NotTo(BeEmpty())
	})

	It("shows the controller-created run for the one-shot scan", func() {
		var lastRunName string
		Eventually(func(g Gomega) {
			got, err := adminClient.GetSecurityScanConfig(ctx, connect.NewRequest(&platform.GetSecurityScanConfigRequest{
				Namespace: scanNS, Name: scanName,
			}))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Msg.GetLastError()).To(BeEmpty(), "controller reported an error")
			g.Expect(got.Msg.GetRunsCreated()).To(BeNumerically(">=", 1))
			g.Expect(got.Msg.GetLastRunName()).NotTo(BeEmpty())
			lastRunName = got.Msg.GetLastRunName()
		}, 3*time.Minute, 5*time.Second).Should(Succeed(),
			"controller never created an AgentRun for the scan (scheme wiring or RBAC drift)")

		By("verifying the AgentRun object exists (manager RBAC create on agentruns)")
		cmd := exec.Command("kubectl", "get", "agentruns.platform.gratefulagents.dev",
			lastRunName, "-n", scanNS)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "AgentRun created by the controller was not found")

		By("verifying the run is visible through the dashboard")
		runs, err := adminClient.ListAgentRuns(ctx, connect.NewRequest(&platform.ListAgentRunsRequest{
			Namespace: scanNS,
		}))
		Expect(err).NotTo(HaveOccurred(), "ListAgentRuns failed")
		names := make([]string, 0, len(runs.Msg.GetRuns()))
		for _, run := range runs.Msg.GetRuns() {
			names = append(names, run.GetName())
		}
		Expect(names).To(ContainElement(lastRunName))

		By("verifying the security overview aggregates without error")
		overview, err := adminClient.GetSecurityOverview(ctx, connect.NewRequest(&platform.GetSecurityOverviewRequest{
			Namespace: scanNS,
		}))
		Expect(err).NotTo(HaveOccurred(), "GetSecurityOverview failed")
		Expect(overview.Msg.GetStoreSupported()).To(BeTrue(), "Postgres store should support security data")
		Expect(overview.Msg.GetConfigCount()).To(BeNumerically(">=", 1))
	})

	It("suspends and resumes the scan through the dashboard", func() {
		By("suspending")
		_, err := adminClient.UpdateSecurityScan(ctx, connect.NewRequest(&platform.UpdateSecurityScanRequest{
			Namespace: scanNS, Name: scanName, Spec: e2eSecurityScanSpec(true),
		}))
		Expect(err).NotTo(HaveOccurred(), "UpdateSecurityScan(suspend) failed")
		Eventually(func(g Gomega) {
			got, err := adminClient.GetSecurityScanConfig(ctx, connect.NewRequest(&platform.GetSecurityScanConfigRequest{
				Namespace: scanNS, Name: scanName,
			}))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Msg.GetPhase()).To(Equal("Suspended"))
			g.Expect(got.Msg.GetConditionReady()).To(Equal("False"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "scan never became Suspended (status RBAC drift)")

		By("resuming")
		_, err = adminClient.UpdateSecurityScan(ctx, connect.NewRequest(&platform.UpdateSecurityScanRequest{
			Namespace: scanNS, Name: scanName, Spec: e2eSecurityScanSpec(false),
		}))
		Expect(err).NotTo(HaveOccurred(), "UpdateSecurityScan(resume) failed")
		Eventually(func(g Gomega) {
			got, err := adminClient.GetSecurityScanConfig(ctx, connect.NewRequest(&platform.GetSecurityScanConfigRequest{
				Namespace: scanNS, Name: scanName,
			}))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Msg.GetPhase()).NotTo(Equal("Suspended"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "scan never resumed")
	})

	It("denies a member user access to another user's namespace", func() {
		By("provisioning a non-admin user directly in the auth store")
		memberPassword := "e2e-member-password"
		hash, err := bcrypt.GenerateFromPassword([]byte(memberPassword), bcrypt.MinCost)
		Expect(err).NotTo(HaveOccurred())
		insert := fmt.Sprintf(
			`INSERT INTO auth_users (username, name, password_hash, role) VALUES ('e2e-member', 'E2E Member', '%s', 'member') ON CONFLICT (username) DO NOTHING;`,
			string(hash))
		cmd := exec.Command("kubectl", "exec", "deployment/e2e-postgres", "-n", namespace,
			"--", "psql", "-U", "postgres", "-d", "postgres", "-c", insert)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to insert member user")

		By("logging in as the member user")
		var memberToken string
		Eventually(func(g Gomega) {
			token, err := dashboardLogin(ctx, "e2e-member", memberPassword)
			g.Expect(err).NotTo(HaveOccurred())
			memberToken = token
		}, time.Minute, 3*time.Second).Should(Succeed(), "Member login failed")
		memberClient = newDashboardClient(memberToken)

		By("rejecting a scan created in the admin's namespace")
		_, err = memberClient.CreateSecurityScan(ctx, connect.NewRequest(&platform.CreateSecurityScanRequest{
			Namespace: scanNS,
			Name:      "e2e-member-scan",
			Spec:      e2eSecurityScanSpec(false),
		}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodePermissionDenied),
			"member CreateSecurityScan in a foreign namespace must be denied, got: %v", err)

		By("rejecting an update to the admin's scan")
		_, err = memberClient.UpdateSecurityScan(ctx, connect.NewRequest(&platform.UpdateSecurityScanRequest{
			Namespace: scanNS, Name: scanName, Spec: e2eSecurityScanSpec(true),
		}))
		Expect(err).To(HaveOccurred(), "member UpdateSecurityScan on a foreign scan must fail")
		Expect(connect.CodeOf(err)).To(BeElementOf(connect.CodePermissionDenied, connect.CodeNotFound),
			"unexpected code: %v", err)
	})

	It("deletes the scan through the dashboard", func() {
		_, err := adminClient.DeleteSecurityScan(ctx, connect.NewRequest(&platform.DeleteSecurityScanRequest{
			Namespace: scanNS, Name: scanName,
		}))
		Expect(err).NotTo(HaveOccurred(), "DeleteSecurityScan failed")

		By("verifying the resource is removed (finalizer cleanup completed)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "securityscans.triggers.gratefulagents.dev",
				scanName, "-n", scanNS)
			out, err := utils.Run(cmd)
			g.Expect(err).To(HaveOccurred())
			g.Expect(out).To(ContainSubstring("NotFound"))
		}, 3*time.Minute, 5*time.Second).Should(Succeed(), "SecurityScan was not deleted")
	})
})

// e2eSecurityScanSpec is a minimal one-shot scan spec whose defaults pass
// trigger validation without live credentials (the secret ref only has to be
// named; the run pod may fail later, which this smoke test does not exercise).
func e2eSecurityScanSpec(suspend bool) *platform.SecurityScanConfigSpec {
	return &platform.SecurityScanConfigSpec{
		RepoUrl: "https://github.com/octocat/Hello-World.git",
		Suspend: suspend,
		Defaults: &platform.AgentRunDefaults{
			Provider: "anthropic",
			AuthMode: "api-key",
			ProviderKeys: []*platform.ProviderKeyRef{
				{Provider: "anthropic", SecretName: "e2e-fake-anthropic", SecretKey: "api-key"},
			},
		},
	}
}

// dashboardLogin authenticates against the port-forwarded dashboard and
// returns an access token.
func dashboardLogin(ctx context.Context, username, password string) (string, error) {
	client := authconnect.NewAuthServiceClient(http.DefaultClient, dashboardBaseURL)
	resp, err := client.Login(ctx, connect.NewRequest(&authpb.LoginRequest{
		Username: username,
		Password: password,
	}))
	if err != nil {
		return "", err
	}
	return resp.Msg.GetAccessToken(), nil
}

// newDashboardClient builds a PlatformService client that sends the given
// bearer token on every request.
func newDashboardClient(token string) platformconnect.PlatformServiceClient {
	interceptor := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	})
	return platformconnect.NewPlatformServiceClient(http.DefaultClient, dashboardBaseURL,
		connect.WithInterceptors(interceptor))
}
