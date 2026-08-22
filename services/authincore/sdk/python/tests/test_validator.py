import unittest

from authincore import AuthinCoreError, Claims, bearer_token, require_scopes


class HelpersTest(unittest.TestCase):
    def test_bearer_token(self):
        self.assertEqual(bearer_token({"Authorization": "Bearer abc"}), "abc")
        with self.assertRaises(AuthinCoreError) as caught:
            bearer_token({})
        self.assertEqual(caught.exception.code, "unauthenticated")

    def test_scope_enforcement(self):
        claims = Claims("identity", "org-a", "issuer", ("aud",), 1, scopes=("db:read",))
        require_scopes(claims, "db:read")
        with self.assertRaises(AuthinCoreError) as caught:
            require_scopes(claims, "db:write")
        self.assertEqual(caught.exception.code, "forbidden")


if __name__ == "__main__":
    unittest.main()
