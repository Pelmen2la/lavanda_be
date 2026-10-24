from pathlib import Path
import tempfile
import unittest

from import_mail_env import import_mail_settings, merge_mail_settings


INCOMING = '''SMTP_HOST=smtp.mail.ru
SMTP_PORT=465
SMTP_USER=sender@example.test
SMTP_PASSWORD="test$pass!@=word"
SMTP_FROM="Лаванда <sender@example.test>"
ADMIN_NOTIFICATION_EMAIL=
'''


class MailImportTest(unittest.TestCase):
    def test_replaces_duplicates_and_preserves_database_and_domain(self):
        existing = "# production\nDATABASE_URL=postgres://production/db\nPUBLIC_URL=https://lavanda-beauty.ru\nSMTP_PORT=587\nSMTP_PORT=25\nADMIN_NOTIFICATION_EMAIL=old@example.test\n"
        merged = merge_mail_settings(existing, INCOMING)
        self.assertIn("DATABASE_URL=postgres://production/db\n", merged)
        self.assertIn("PUBLIC_URL=https://lavanda-beauty.ru\n", merged)
        self.assertEqual(merged.count("SMTP_PORT="), 1)
        self.assertIn(INCOMING, merged)

    def test_rejects_unrelated_or_incomplete_input(self):
        for incoming in [INCOMING + "DATABASE_URL=wrong\n", INCOMING + "SMTP_PORT=25\n", "SMTP_HOST=smtp.mail.ru\n", INCOMING.replace('SMTP_PASSWORD="test$pass!@=word"', 'SMTP_PASSWORD=')]:
            with self.assertRaises(ValueError):
                merge_mail_settings("DATABASE_URL=original\n", incoming)

    def test_import_accepts_windows_encoding_and_creates_backup(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "mail.env"
            target = Path(directory) / "production.env"
            original = "DATABASE_URL=original\nCOOKIE_SECURE=true\n"
            source.write_bytes(b"\xef\xbb\xbf" + INCOMING.replace("\n", "\r\n").encode("utf-8"))
            target.write_text(original, encoding="utf-8")
            backup = import_mail_settings(source, target)
            self.assertEqual(backup.read_text(encoding="utf-8"), original)
            self.assertEqual(target.read_text(encoding="utf-8"), original + INCOMING)
            source.write_text("DATABASE_URL=wrong\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                import_mail_settings(source, target)
            self.assertEqual(target.read_text(encoding="utf-8"), original + INCOMING)


if __name__ == "__main__":
    unittest.main()
