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
        # Cognito messages can include user data. Report only the status.
        raise LoginError(
            f"Cognito sign-in failed (HTTP {error.code}); check the CI user's "
            "credentials, app client, and ALLOW_USER_PASSWORD_AUTH configuration"
        ) from None
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
