"""NewsML mapping, safety and limits at the SDK invocation boundary."""
import json
import tempfile
import unittest
from pathlib import Path

from quivr_plugin.testing import build_request, expect_response, invoke_fixture
from newsml_g2.normalizer import plugin

ROOT = Path(__file__).resolve().parent.parent
NAR = 'http://iptc.org/std/nar/2006-10-01/'


class Normalizer(unittest.TestCase):
    def invoke(self, xml, configuration=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / 'item.xml').write_bytes(xml.encode() if isinstance(xml, str) else xml)
            (path / 'fixture.json').write_text(json.dumps({
                'input': {'path': 'item.xml', 'media_type': 'application/vnd.iptc.g2.newsitem+xml'},
                'configuration': configuration or {},
            }))
            return invoke_fixture(plugin, path / 'fixture.json')

    def test_multilingual_golden_mapping_and_source(self):
        fixture = ROOT / 'fixtures' / 'sample.json'
        response = expect_response(invoke_fixture(plugin, fixture))
        actual = response.to_dict()
        source = actual['manifest']['parts'].pop()
        self.assertEqual(source['content']['blob_id'], build_request(fixture).input.blob_id)
        self.assertEqual(source['role'], 'source')
        self.assertEqual(actual, json.loads((ROOT / 'tests' / 'data' / 'sample.expected.json').read_text()))

    def test_optional_metadata_and_namespace_collisions(self):
        xml = f'<newsItem xmlns="{NAR}" xmlns:x="urn:example:wire" guid="urn:example:minimal" version="1"><contentMeta><x:headline>False title</x:headline></contentMeta><contentSet><inlineXML xml:lang="he"><p>שלום</p></inlineXML></contentSet></newsItem>'
        response = expect_response(self.invoke(xml))
        self.assertEqual(response.language, 'he')
        self.assertEqual([p.role for p in response.manifest.parts], ['body', 'source'])
        document = response.extensions['newsml-g2.document'].data
        self.assertEqual(document, {'guid': 'urn:example:minimal', 'version': '1', 'language': 'he'})
        tree = response.extensions['newsml-g2.xml'].data['root']
        self.assertEqual(tree['children'][0]['children'][0]['name'], '{urn:example:wire}headline')

    def test_single_item_message_keeps_wrapper_and_selected_headers(self):
        xml = f'<newsMessage xmlns="{NAR}" xmlns:x="urn:example:wire"><header><x:delivery id="a">first</x:delivery><x:delivery id="b">second</x:delivery></header><itemSet><newsItem guid="urn:example:1" version="2" xml:lang="ar"><contentMeta><headline>خبر</headline></contentMeta></newsItem></itemSet></newsMessage>'
        response = expect_response(self.invoke(xml, {'header_paths': ['header/{urn:example:wire}delivery']}))
        self.assertEqual(response.extensions['newsml-g2.document'].data['guid'], 'urn:example:1')
        headers = response.extensions['newsml-g2.headers'].data['paths']
        self.assertEqual([v['text'] for v in headers['header/{urn:example:wire}delivery']], ['first', 'second'])
        self.assertEqual(response.extensions['newsml-g2.xml'].data['root']['name'], f'{{{NAR}}}newsMessage')

    def test_nitf_message_uses_inherited_language_and_mixed_text(self):
        response = expect_response(invoke_fixture(plugin, ROOT / 'fixtures' / 'message.json'))
        self.assertEqual(response.language, 'en')
        self.assertEqual([p.content.text for p in response.manifest.parts if p.content.kind == 'text'],
                         ['Ferry timetable updated', 'The ferry departs at noon.'])
        self.assertEqual(response.extensions['newsml-g2.document'].data['signals'], [{'qcode': 'sig:update'}])

    def test_invalid_and_unsafe_xml_is_terminal(self):
        cases = [
            ('<broken>', 'invalid_xml'),
            ('<!DOCTYPE newsItem [<!ENTITY e SYSTEM "file:///etc/passwd">]><newsItem>&e;</newsItem>', 'unsafe_xml'),
            ('<!DOCTYPE newsItem><newsItem/>', 'unsafe_xml'),
            ('<newsItem/>', 'unsupported_document'),
            (f'<newsMessage xmlns="{NAR}"><itemSet/></newsMessage>', 'item_count'),
            (f'<newsMessage xmlns="{NAR}"><itemSet><newsItem/><newsItem/></itemSet></newsMessage>', 'item_count'),
        ]
        for xml, code in cases:
            with self.subTest(code=code, xml=xml):
                reply = self.invoke(xml)
                self.assertEqual((reply.status, reply.body['code'], reply.body['retryable']), (422, code, False))

    def test_input_tree_and_output_limits_fail_without_silent_loss(self):
        for xml, config, code in [
            (f'<newsItem xmlns="{NAR}">' + 'x' * 1024 + '</newsItem>', {'max_input_bytes': 1024}, 'input_too_large'),
            (f'<newsItem xmlns="{NAR}">' + '<x>' * 65 + '</x>' * 65 + '</newsItem>', {}, 'xml_too_complex'),
            (f'<newsItem xmlns="{NAR}">' + '<x/>' * 10000 + '</newsItem>', {}, 'xml_too_complex'),
            (f'<newsItem xmlns="{NAR}"><contentSet><inlineXML><p>' + 'ع' * 600 + '</p></inlineXML></contentSet></newsItem>', {'max_text_bytes': 1024}, 'text_too_large'),
            (f'<newsItem xmlns="{NAR}"><itemMeta><x a="' + '\\' * 700000 + '"/></itemMeta></newsItem>', {'header_paths': ['itemMeta/x']}, 'manifest_too_large'),
        ]:
            with self.subTest(code=code):
                reply = self.invoke(xml, config)
                self.assertEqual((reply.status, reply.body['code']), (422, code))

    def test_limits_accept_exact_boundaries(self):
        prefix = f'<newsItem xmlns="{NAR}">'
        suffix = '</newsItem>'
        xml = prefix + 'x' * (1024 - len(prefix.encode()) - len(suffix)) + suffix
        self.assertEqual(self.invoke(xml, {'max_input_bytes': 1024}).status, 200)
        xml = prefix + '<x>' * 63 + '</x>' * 63 + suffix
        self.assertEqual(self.invoke(xml).status, 200)
        xml = prefix + '<x/>' * 9999 + suffix
        self.assertEqual(self.invoke(xml).status, 200)
        xml = prefix + '<contentSet><inlineXML><p>' + 'ع' * 512 + '</p></inlineXML></contentSet>' + suffix
        self.assertEqual(self.invoke(xml, {'max_text_bytes': 1024}).status, 200)

    def test_alternating_languages_cannot_be_silently_combined(self):
        xml = f'<newsItem xmlns="{NAR}"><contentSet><inlineXML>' + ''.join(
            f'<p xml:lang="{("en", "ar")[n % 2]}">Text {n}</p>' for n in range(65)
        ) + '</inlineXML></contentSet></newsItem>'
        reply = self.invoke(xml)
        self.assertEqual((reply.status, reply.body['code']), (422, 'too_many_text_parts'))

    def test_common_metadata_dates_and_missing_fields(self):
        for first, version, expected in [
            ('2026-01-02T12:00:00+02:00', '', '2026-01-02T10:00:00Z'),
            ('bad date', '2026-01-03T00:00:00Z', '2026-01-03T00:00:00Z'),
            ('2026-01-02T12:00:00', '', None),
        ]:
            with self.subTest(first=first):
                xml = f'<newsItem xmlns="{NAR}"><itemMeta><firstCreated>{first}</firstCreated><versionCreated>{version}</versionCreated></itemMeta></newsItem>'
                response = expect_response(self.invoke(xml))
                common = response.extensions['newsml-g2.metadata'].data
                self.assertEqual(common, {'source_type': 'newswire', **({'published_at': expected} if expected else {})})
                self.assertEqual(response.extensions['newsml-g2.document'].data['first_created'], first)

    def test_paragraph_grouping_preserves_order_language_and_boundaries(self):
        xml = f'<newsItem xmlns="{NAR}" xml:lang="en"><contentMeta><headline>Title</headline></contentMeta><contentSet><inlineXML><p>A <b>bold</b> tail</p>' + ''.join(f'<p>{n}</p>' for n in range(70)) + '<p xml:lang="ar" dir="rtl">خبر</p></inlineXML></contentSet></newsItem>'
        response = expect_response(self.invoke(xml))
        text_parts = [p for p in response.manifest.parts if p.content.kind == 'text']
        self.assertLessEqual(len(text_parts), 64)
        self.assertEqual('\n\n'.join(p.content.text for p in text_parts[1:]), 'A bold tail\n\n' + '\n\n'.join(map(str, range(70))) + '\n\nخبر')
        self.assertEqual(text_parts[-1].extensions['newsml-g2.text'].data, {'language': 'ar', 'dir': 'rtl'})

        # Omitted direction means ltr in NewsML-G2, so spelling it explicitly
        # on alternating paragraphs must not create incompatible text groups.
        xml = f'<newsItem xmlns="{NAR}" xml:lang="en"><contentSet><inlineXML>' + ''.join(
            f'<p{("", " dir=\"ltr\"")[n % 2]}>{n}</p>' for n in range(65)
        ) + '</inlineXML></contentSet></newsItem>'
        response = expect_response(self.invoke(xml))
        bodies = [p for p in response.manifest.parts if p.role == 'body']
        self.assertEqual(len(bodies), 64)
        self.assertEqual('\n\n'.join(p.content.text for p in bodies), '\n\n'.join(map(str, range(65))))

    def test_header_paths_are_literal_and_configuration_is_validated(self):
        xml = f'<newsItem xmlns="{NAR}"/>'
        for path in ['../itemMeta', 'header/*', 'header/x[1]', 'header//x']:
            with self.subTest(path=path):
                reply = self.invoke(xml, {'header_paths': [path]})
                self.assertEqual(reply.body['code'], 'invalid_configuration')
