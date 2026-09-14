import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error


spec = importlib.util.spec_from_file_location("cognito_login", Path(__file__).with_name("cognito-login.py"))
login = importlib.util.module_from_spec(spec)
spec.loader.exec_module(login)


class CognitoLoginTests(unittest.TestCase):
    def test_password_auth_uses_cli_client_and_returns_access_token(self):
        def respond(request, timeout):
            self.assertEqual(request.full_url, "https://cognito-idp.us-east-1.amazonaws.com/")
            self.assertEqual(request.get_header("X-amz-target"), "AWSCognitoIdentityProviderService.InitiateAuth")
            self.assertEqual(json.loads(request.data), {
                "AuthFlow": "USER_PASSWORD_AUTH",
                "ClientId": "cli_client",
                "AuthParameters": {"USERNAME": "ci@example.com", "PASSWORD": "quotes'\"$()\n"},
            })
            self.assertEqual(timeout, 30)
            return io.BytesIO(json.dumps({"AuthenticationResult": {
                "AccessToken": "access-token", "IdToken": "id-token", "RefreshToken": "refresh-token",
            }}).encode())

        self.assertEqual(login.authenticate("us-east-1", "cli_client", "ci@example.com", "quotes'\"$()\n",
                                           opener=respond), "access-token")

    def test_challenges_and_id_tokens_are_not_accepted_as_access_tokens(self):
        for result in [
            {"ChallengeName": "SOFTWARE_TOKEN_MFA", "Session": "private-session"},
            {"ChallengeName": "NEW_PASSWORD_REQUIRED"},
            {"AuthenticationResult": {"IdToken": "id-token"}},
            {"AuthenticationResult": {"AccessToken": "first\nsecond"}},
        ]:
            with self.subTest(result=result), self.assertRaises(login.LoginError):
                login.authenticate("us-east-1", "client", "user", "password",
                                   opener=lambda *args, **kwargs: io.BytesIO(json.dumps(result).encode()))

    def test_cognito_errors_do_not_echo_credentials(self):
        def fail(*args, **kwargs):
            raise urllib.error.HTTPError("https://cognito-idp.us-east-1.amazonaws.com/", 400,
                                         "password-in-error", {}, io.BytesIO(b"password-in-error"))

        with self.assertRaises(login.LoginError) as error:
            login.authenticate("us-east-1", "client", "user", "password-in-error", opener=fail)
        self.assertNotIn("password-in-error", str(error.exception))

    def test_token_file_is_private_and_github_output_is_masked(self):
        with tempfile.TemporaryDirectory() as directory:
            token_file = Path(directory) / "token"
            stdout = io.StringIO()
            with patch.object(login, "authenticate", return_value="generated-token"), \
                    patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}), \
                    patch("sys.argv", ["cognito-login.py", "--client-id", "client", "--token-file", str(token_file)]), \
                    contextlib.redirect_stdout(stdout):
                self.assertEqual(login.main(), 0)
            self.assertEqual(token_file.read_text(), "generated-token\n")
            self.assertEqual(token_file.stat().st_mode & 0o777, 0o600)
            self.assertEqual(stdout.getvalue(), "::add-mask::generated-token\n")


if __name__ == "__main__":
    unittest.main()
