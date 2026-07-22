package com.gratefulagents.operator.googleoauth

import org.junit.Assert.assertEquals
import org.junit.Test

class GoogleOauthPluginTest {
  @Test
  fun normalizesFormEncodedScopeSeparators() {
    assertEquals(
      "openid email profile",
      normalizeGoogleOauthQueryParameter("scope", "openid+email+profile")
    )
  }

  @Test
  fun preservesPlusInOtherParameters() {
    assertEquals(
      "nonce+with+plus",
      normalizeGoogleOauthQueryParameter("nonce", "nonce+with+plus")
    )
  }
}
