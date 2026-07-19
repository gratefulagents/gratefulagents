import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthService } from "@/rpc/auth/service_pb";
import { createAuthInterceptor } from "./auth-interceptor";
import { createEnvironmentInterceptor } from "./environment-interceptor";
import { createRetryInterceptor } from "./retry-interceptor";
import { backendBaseUrl } from "./platform";
import { getCloudflareAccessHeaders } from "./app-environment";
import { runtimeFetch } from "./runtime-fetch";
import { getAccessToken, notifySessionExpired, refreshAccessToken } from "./client";

export function getAuthClient() {
  const transport = createConnectTransport({
    baseUrl: backendBaseUrl(),
    fetch: runtimeFetch,
    interceptors: [
      createEnvironmentInterceptor(getCloudflareAccessHeaders),
      createRetryInterceptor(),
      createAuthInterceptor(getAccessToken, refreshAccessToken, notifySessionExpired),
    ],
  });
  return createClient(AuthService, transport);
}
