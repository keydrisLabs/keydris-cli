#!/usr/bin/env python3
"""Sign in a CI user with Cognito and write only its short-lived access token.

Uses the public InitiateAuth API; no AWS IAM credentials are required. Passwords
come from the environment, and neither credentials nor token responses are logged.
"""

import argparse
import base64
import hashlib
import hmac
import json
import os
import re
import sys
import urllib.error
import urllib.request


class LoginError(Exception):
    pass


ERROR_HINTS = {
    "NotAuthorizedException": "Cognito rejected the sign-in; check the account credentials and app-client secret configuration.",
    "UserNotFoundException": "The user was not found in the app client's user pool.",
    "UserNotConfirmedException": "The Cognito user must be confirmed before signing in.",
    "PasswordResetRequiredException": "The Cognito user must reset their password before signing in.",
    "InvalidParameterException": "Check the app client and its ALLOW_USER_PASSWORD_AUTH setting.",
    "ResourceNotFoundException": "Check that the app client exists in the configured Cognito region.",
    "TooManyRequestsException": "Cognito rate-limited the request; wait before retrying.",
    "ForbiddenException": "An AWS WAF rule blocked the Cognito request.",
    "InvalidUserPoolConfigurationException": "Check the Cognito user-pool configuration.",
    "UserLambdaValidationException": "A Cognito Lambda trigger rejected the sign-in.",
    "InvalidLambdaResponseException": "A Cognito Lambda trigger returned an invalid response.",
    "UnexpectedLambdaException": "A Cognito Lambda trigger failed.",
    "InternalErrorException": "Cognito encountered an internal service error.",
}

# Use known provider messages only to select fixed explanations. Never print the
# response text: it can contain usernames, client IDs, or custom Lambda output.
REASON_HINTS = (
    ("NotAuthorizedException", "incorrect username or password", "Cognito reports incorrect username or password; it may intentionally hide whether the user exists."),
    ("NotAuthorizedException", "secret_hash was not received", "This app client requires its client-secret GitHub secret so the helper can send SECRET_HASH."),
    ("NotAuthorizedException", "unable to verify secret hash", "The supplied app-client secret hash was rejected; check the client-secret GitHub secret."),
    ("NotAuthorizedException", "user is disabled", "The Cognito user is disabled."),
    ("NotAuthorizedException", "password attempts exceeded", "Cognito temporarily blocked password attempts; wait before retrying."),
    ("InvalidParameterException", "user_password_auth flow not enabled", "Enable ALLOW_USER_PASSWORD_AUTH for this Cognito app client."),
)


def describe_auth_error(error):
    fallback = f"Cognito sign-in failed (HTTP {error.code}); no recognized Cognito error details were returned"
    try:
        detail = json.loads(error.read(8192))
    except (ValueError, OSError):
        return fallback
    if not isinstance(detail, dict):
        return fallback
    code = detail.get("__type", "")
    if not isinstance(code, str):
        return fallback
    code = code.rsplit("#", 1)[-1]
    hint = ERROR_HINTS.get(code)
    if hint is None:
        return fallback
    message = detail.get("message", "")
    if isinstance(message, str):
        for reason_code, phrase, explanation in REASON_HINTS:
            if code == reason_code and phrase in message.lower():
                hint = explanation
                break
    return f"Cognito sign-in failed ({code}; HTTP {error.code}): {hint}"


def authenticate(region, client_id, username, password, client_secret="", opener=None):
    if not re.fullmatch(r"[a-z]{2}(?:-[a-z]+)+-\d+", region):
        raise LoginError("A valid Cognito region is required")
    if not re.fullmatch(r"[\w+]+", client_id, flags=re.ASCII):
        raise LoginError("A Cognito app client ID is required")
    if not username or not password:
        raise LoginError("Set KEYDRIS_E2E_USERNAME and KEYDRIS_E2E_PASSWORD")

    parameters = {"USERNAME": username, "PASSWORD": password}
    if client_secret:
        parameters["SECRET_HASH"] = base64.b64encode(
            hmac.new(
                client_secret.encode(), (username + client_id).encode(), hashlib.sha256
            ).digest()
        ).decode()
    suffix = "amazonaws.com.cn" if region.startswith("cn-") else "amazonaws.com"
    request = urllib.request.Request(
        f"https://cognito-idp.{region}.{suffix}/",
        data=json.dumps({
            "AuthFlow": "USER_PASSWORD_AUTH",
            "ClientId": client_id,
            "AuthParameters": parameters,
        }).encode(),
        headers={
            "Content-Type": "application/x-amz-json-1.1",
            "X-Amz-Target": "AWSCognitoIdentityProviderService.InitiateAuth",
        },
        method="POST",
    )
    try:
        with (opener or urllib.request.urlopen)(request, timeout=30) as response:
            result = json.load(response)
    except urllib.error.HTTPError as error:
        raise LoginError(describe_auth_error(error)) from None
    except (urllib.error.URLError, TimeoutError, ValueError):
        raise LoginError("Could not obtain a valid Cognito authentication response") from None

    if not isinstance(result, dict):
        raise LoginError("Invalid Cognito authentication response")
    if result.get("ChallengeName"):
        # Do not silently bypass MFA or turn a temporary password into a new one.
        raise LoginError(
            "Cognito requires an additional sign-in challenge; this CI flow "
            "supports an already-confirmed username/password user"
        )
    auth = result.get("AuthenticationResult")
    token = auth.get("AccessToken") if isinstance(auth, dict) else None
    if not isinstance(token, str) or not token or any(c.isspace() for c in token):
        raise LoginError("Cognito did not return an access token")
    return token


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--token-file", required=True)
    args = parser.parse_args()
    try:
        token = authenticate(
            os.environ.get("KEYDRIS_COGNITO_REGION", "us-east-1"),
            args.client_id,
            os.environ.get("KEYDRIS_E2E_USERNAME", ""),
            os.environ.get("KEYDRIS_E2E_PASSWORD", ""),
            os.environ.get("KEYDRIS_COGNITO_CLIENT_SECRET", ""),
        )
        if os.environ.get("GITHUB_ACTIONS") == "true":
            # Mask this newly generated value before any later step can log it.
            print("::add-mask::" + token.replace("%", "%25"), flush=True)
        # Exclusive creation avoids following a symlink or leaving an older token
        # available after a failed sign-in. The runner uses a private temp dir.
        with os.fdopen(os.open(args.token_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as output:
            output.write(token + "\n")
    except LoginError as error:
        print(f"cognito-login: {error}", file=sys.stderr)
        return 1
    except OSError:
        print("cognito-login: could not create the access-token file", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
