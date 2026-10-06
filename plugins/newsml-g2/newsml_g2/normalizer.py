"""Map NewsML-G2 items without changing the submitted record identity."""
from __future__ import annotations

import io
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from xml.etree.ElementTree import ParseError

from defusedxml.ElementTree import iterparse
from defusedxml.common import DefusedXmlException
from quivr_plugin import (
    BlobContent, ExtensionEntry, Invocation, ManifestContent, NormalizerResponse,
    Part, Plugin, ResponseWarning, TerminalError, TextContent,
)

NAR = 'http://iptc.org/std/nar/2006-10-01/'
XML_LANG = '{http://www.w3.org/XML/1998/namespace}lang'
MAX_NODES = 10000
MAX_DEPTH = 64
MAX_TEXT_PARTS = 64
MAX_EXTENSIONS_BYTES = 65536
# Three owned namespaces plus shared metadata fit below the engine budget.
MAX_NAMESPACE_BYTES = 12 * 1024
MAX_COMMON_BYTES = 24 * 1024
MAX_CONTEXT_BYTES = 256
plugin = Plugin(Path(__file__).resolve().parent.parent / 'quivr-plugin.yaml')


def _tag(name):
    return f'{{{NAR}}}{name}'


def _parse(data):
    """Bound the tree as it is constructed; DTDs and entities are forbidden."""
    depth = nodes = 0
    try:
        parser = iterparse(io.BytesIO(data), events=('start', 'end'), forbid_dtd=True,
                           forbid_entities=True, forbid_external=True)
        for event, _ in parser:
            if event == 'start':
                depth += 1
                nodes += 1
                if depth > MAX_DEPTH or nodes > MAX_NODES:
                    raise TerminalError('xml_too_complex', 'XML exceeds 64 levels or 10000 elements')
            else:
                depth -= 1
        return parser.root
    except DefusedXmlException as exc:
        raise TerminalError('unsafe_xml', 'DTDs and XML entities are forbidden') from exc
    except ParseError as exc:
        raise TerminalError('invalid_xml', 'The source is not well-formed XML') from exc


def _tree(element):
    """Expanded names avoid collisions; text/tail preserve mixed content order."""
    result = {'name': element.tag}
    if element.attrib:
        result['attributes'] = dict(element.attrib)
    if element.text is not None:
        result['text'] = element.text
    if element.tail is not None:
        result['tail'] = element.tail
    if len(element):
        result['children'] = [_tree(child) for child in element]
    return result


def _text(element):
    return ''.join(element.itertext()).strip()


def _concept(element):
    result = dict(element.attrib)
    names = [_text(child) for child in element.findall(_tag('name'))]
    if names:
        result['names'] = names
    value = (element.text or '').strip()
    if value:
        result['value'] = value
    return result


def _metadata(item, language):
    result = {key: item.attrib[key] for key in ('guid', 'version') if key in item.attrib}
    if language:
        result['language'] = language
    for path, key in [('itemMeta/firstCreated', 'first_created'),
                      ('itemMeta/versionCreated', 'version_created'),
                      ('contentMeta/urgency', 'urgency')]:
        node = item.find(path, {'': NAR})
        if node is not None:
            result[key] = _text(node)
    for path, key in [('itemMeta/provider', 'provider'), ('itemMeta/signal', 'signals'),
                      ('contentMeta/genre', 'genres'), ('contentMeta/subject', 'subjects'),
                      ('contentMeta/located', 'located'), ('contentMeta/creator', 'creators')]:
        values = [_concept(node) for node in item.findall(path, {'': NAR})]
        if values:
            result[key] = values
    keywords = [_text(node) for node in item.findall('contentMeta/keyword', {'': NAR})]
    if keywords:
        result['keywords'] = keywords
    roles = list(dict.fromkeys(node.attrib['role'] for node in item.iter() if 'role' in node.attrib))
    if roles:
        result['roles'] = roles
    return result


def _common_metadata(item, document):
    """Bound the shared filter view; complete values remain in source metadata."""
    def bounded(value):
        return value.strip()[:200]

    def distinct(values):
        return list(dict.fromkeys(value for raw in values if (value := bounded(raw))))[:50]

    result = {'source_type': 'news_item'}
    if language := bounded(document.get('language', '')):
        result['language'] = language
    for field in ('first_created', 'version_created'):
        if value := document.get(field):
            match = re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:[0-5]\d(\.\d{1,9})?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)', value)
            if not match:
                continue
            try:
                date = datetime.fromisoformat(value.replace('Z', '+00:00'))
                utc = date.astimezone(timezone.utc).isoformat(timespec='seconds')
                # Keep the source's subsecond precision, including nanoseconds.
                result['published_at'] = utc.removesuffix('+00:00') + (match[1] or '') + 'Z'
                break
            except (ValueError, OverflowError):
                pass  # The original value remains in document and XML metadata.
    def identity(concept):
        candidates = [concept.get('uri', ''), concept.get('qcode', ''),
                      *concept.get('names', []), concept.get('value', '')]
        return next((value for raw in candidates if (value := bounded(raw))), '')
    if providers := document.get('provider'):
        if source := bounded(identity(providers[0])):
            result['source'] = source
    for source, target in [('creators', 'author'), ('subjects', 'subjects')]:
        values = [identity(value) for value in document.get(source, [])]
        if values := distinct(values):
            result[target] = values
    if tags := document.get('keywords'):
        if values := distinct(tags):
            result['tags'] = values
    places, countries = [], []
    geo = item.findall('contentMeta/located', {'': NAR})
    geo += [node for node in item.findall('contentMeta/subject', {'': NAR})
            if node.attrib.get('type', '').endswith(':geoArea')]
    for located in geo:
        concept = _concept(located)
        places.extend(distinct(concept.get('names', [])) or [identity(concept)])
        for node in located.iter():
            code = node.attrib.get('qcode', '')
            if code.startswith('iso3166-1a2:'):
                countries.append(code.split(':', 1)[1])
    for values, field in [(places, 'place'), (countries, 'country')]:
        if values := distinct(values):
            result[field] = values
    return result


def _selected_headers(root, paths):
    result = {}
    for path in paths:
        # Tokenize expanded names without splitting slashes inside namespace URIs.
        names = re.findall(r'(?:\{[^{}]+\})?[A-Za-z_][A-Za-z0-9_.-]*', path)
        nodes = [root]
        for name in names:
            tag = name if name.startswith('{') else _tag(name)
            nodes = [child for parent in nodes for child in parent if child.tag == tag]
        result[path] = [_tree(node) for node in nodes]
    return {'paths': result}


def _extension(data):
    return ExtensionEntry(schema_version='1', data=data)


def _json_bytes(value):
    """Match Go encoding/json's UTF-8 size, including its default HTML escapes."""
    encoded = json.dumps(value, ensure_ascii=False, separators=(',', ':'))
    for char in ('&', '<', '>', '\u2028', '\u2029'):
        encoded = encoded.replace(char, f'\\u{ord(char):04x}')
    return len(encoded.encode('utf-8'))


def _bound_fields(data, budget, *, marker=False):
    """Keep a deterministic prefix of fields; source_type is required metadata."""
    if _json_bytes(data) <= budget:
        return False
    keys = list(data)
    if marker:
        data['truncated'] = True
    for key in reversed(keys):
        if _json_bytes(data) <= budget:
            break
        if key != 'source_type':
            del data[key]
    return True


@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    source = invocation.request.input
    if source.size_bytes > invocation.configuration.get('max_input_bytes', 1048576):
        raise TerminalError('input_too_large', 'XML exceeds max_input_bytes; submit a smaller item')
    root = _parse(invocation.read_input())
    if root.tag == _tag('newsItem'):
        items = [root]
    elif root.tag == _tag('newsMessage'):
        items = root.findall('itemSet/newsItem', {'': NAR})
        if not items or len(items) != len(root.findall('itemSet/*', {'': NAR})):
            raise TerminalError('item_count', 'A message must contain one or more newsItems and no other item types')
    else:
        raise TerminalError('unsupported_document', 'Expected a NewsML-G2 namespaced newsItem or newsMessage')

    # xml:lang and dir inherit through all ancestors, including the message.
    contexts = {}
    def visit(node, inherited):
        context = dict(inherited)
        if XML_LANG in node.attrib:
            context['language'] = node.attrib[XML_LANG]
        if 'dir' in node.attrib:
            context['dir'] = node.attrib['dir']
        contexts[node] = context
        for child in node:
            visit(child, context)
    visit(root, {})

    candidates = []
    languages = []
    title_found = False
    for index, item in enumerate(items, 1):
        item_candidates = []
        # Keep bare/single-item keys stable; prefix multi-item keys by position.
        prefix = f'item-{index}-' if len(items) > 1 else ''
        for name in ('headline', 'slugline'):
            for n, node in enumerate(item.findall(f'contentMeta/{name}', {'': NAR}), 1):
                if text := _text(node):
                    role = 'title' if name == 'headline' and not title_found else 'body'
                    title_found = title_found or role == 'title'
                    item_candidates.append((f'{prefix}{name}-{n}', role, text, contexts[node]))
        number = 0
        for inline in item.findall('contentSet/inlineXML', {'': NAR}):
            for node in inline.iter():
                if node.tag in {'p', '{http://www.w3.org/1999/xhtml}p',
                                '{http://iptc.org/std/NITF/2006-10-18/}p'} and (text := _text(node)):
                    number += 1
                    item_candidates.append((f'{prefix}paragraph-{number}', 'body', text, contexts[node]))
        language = contexts[item].get('language')
        if not language:
            language_node = item.find('contentMeta/language', {'': NAR})
            language = language_node.attrib.get('tag') if language_node is not None else None
        if not language:
            language = next((context.get('language') for _, _, _, context in item_candidates
                             if context.get('language')), None)
        if language:
            for _, _, _, context in item_candidates:
                context.setdefault('language', language)
        languages.append(language)
        candidates.extend(item_candidates)
    # The Version's advisory metadata describes its first item.
    item, language = items[0], languages[0]

    # Merge only adjacent bodies with the same language/direction. Never mix title
    # semantics or languages just to fit the engine's indexing limit.
    merged = len(candidates) > MAX_TEXT_PARTS
    if merged:
        runs = []
        for candidate in candidates:
            if (runs and candidate[1] == runs[-1][0][1] == 'body'
                    and candidate[3].get('language') == runs[-1][0][3].get('language')
                    and candidate[3].get('dir', 'ltr') == runs[-1][0][3].get('dir', 'ltr')):
                runs[-1].append(candidate)
            else:
                runs.append([candidate])
        if len(runs) > MAX_TEXT_PARTS:
            raise TerminalError('too_many_text_parts', 'More than 64 text Parts cannot be grouped without mixing languages or roles')
        candidates = []
        for index, run in enumerate(runs):
            keep = min(len(run), MAX_TEXT_PARTS - len(candidates) - (len(runs) - index - 1))
            candidates.extend(run[:keep - 1])
            key, role, _, context = run[keep - 1]
            candidates.append((key, role, '\n\n'.join(entry[2] for entry in run[keep - 1:]), context))
    if sum(len(text.encode('utf-8')) for _, _, text, _ in candidates) > invocation.configuration.get('max_text_bytes', 262144):
        raise TerminalError('text_too_large', 'Text exceeds max_text_bytes; submit a smaller item')
    context_truncated = False
    for _, _, _, context in candidates:
        context_truncated = _bound_fields(context, MAX_CONTEXT_BYTES, marker=True) or context_truncated
    parts = [Part(key=key, role=role, content=TextContent(text=text),
                  extensions={'newsml-g2.text': _extension(context)} if context else None)
             for key, role, text, context in candidates]
    parts.append(Part(key='source', role='source',
                      content=BlobContent(blob_id=source.blob_id, media_type=source.media_type)))
    document = _metadata(item, language)
    extensions = {'newsml-g2.document': _extension(document),
                  'quivr.metadata': _extension(_common_metadata(item, document)),
                  'newsml-g2.xml': _extension({'root': _tree(root)})}
    if paths := invocation.configuration.get('header_paths'):
        extensions['newsml-g2.headers'] = _extension(_selected_headers(root, paths))
    warnings = []
    if _json_bytes({key: value.to_dict() for key, value in extensions.items()}) > MAX_EXTENSIONS_BYTES:
        # Omit oversize trees as a whole, keeping required schema fields and a
        # marker. Small trees and metadata remain unchanged. The raw Blob is
        # always the complete source, independent of these advisory views.
        for namespace, entry in extensions.items():
            budget = MAX_COMMON_BYTES if namespace == 'quivr.metadata' else MAX_NAMESPACE_BYTES
            if _json_bytes(entry.to_dict()) <= budget:
                continue
            if namespace == 'newsml-g2.xml':
                entry.data = {'root': {}, 'truncated': True}
            elif namespace == 'newsml-g2.headers':
                entry.data = {'paths': {}, 'truncated': True}
            else:
                _bound_fields(entry.data, budget - 128,
                              marker=namespace != 'quivr.metadata')
        context_truncated = True
    if context_truncated:
        warnings.append(ResponseWarning(code='extensions_truncated', message='Oversize advisory metadata was omitted within the extension JSON budget; the source Blob retains the complete XML'))
    if merged:
        warnings.append(ResponseWarning(code='paragraphs_grouped', message='Adjacent body Parts were grouped within 64 text Parts'))
    if not candidates:
        warnings.append(ResponseWarning(code='no_text_parts', message='No headline, slugline or supported paragraph text was found; only the source Blob is retained'))
    response = NormalizerResponse(manifest=ManifestContent(parts=parts), extensions=extensions,
                                  language=language or None,
                                  warnings=warnings or None)
    if len(json.dumps(response.to_dict(), ensure_ascii=False, separators=(',', ':')).encode()) > invocation.manifest.max_response_bytes:
        raise TerminalError('manifest_too_large', 'Normalized output exceeds the declared response byte limit')
    return response
