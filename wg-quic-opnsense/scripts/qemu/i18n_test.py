"""Keep user-facing plugin vocabulary complete without translating configuration."""
import json
from pathlib import Path
import re
import unittest
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2] / 'net/wg-quic/src/opnsense'


class TranslationCoverage(unittest.TestCase):
    def test_catalog_covers_forms_views_api_and_widget(self):
        catalog = json.loads((ROOT / 'www/js/wg-quic/locales/zh.json').read_text())
        required = set()
        for source in ROOT.rglob('*'):
            if source.suffix in ('.volt', '.php', '.js'):
                required.update(re.findall(
                    r"(?:lang\._|gettext|this\.translate)\('([^']*)'\)", source.read_text()))
            if source.suffix == '.xml' and ('forms' in source.parts or 'models' in source.parts):
                tree = ET.parse(source)
                for node in tree.iter():
                    if node.tag in ('label', 'help', 'hint', 'ValidationMessage') and node.text:
                        required.add(node.text.strip())
                for options in tree.findall('.//OptionValues'):
                    required.update(node.text.strip() for node in options if node.text)
        # Algorithm names and protocol abbreviations deliberately remain unchanged.
        invariant = {'CUBIC', 'Reno', 'Salamander', 'MTU', 'FEC'}
        self.assertEqual(required - catalog.keys() - invariant, set())
        for key, value in catalog.items():
            self.assertTrue(value.strip(), key)
            self.assertEqual(re.findall(r'\{\w+\}', key), re.findall(r'\{\w+\}', value), key)
