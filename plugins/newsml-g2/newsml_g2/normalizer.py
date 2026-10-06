"""Map one NewsML-G2 item without changing the submitted record identity."""
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
MAX_RESPONSE_BYTES = 2097151
MAX_TEXT_PARTS = 64
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
    """THE-1166's common-field shape, retained in our owned namespace on API 0.1."""
    result = {'source_type': 'newswire'}
    if language := document.get('language'):
        result['language'] = language
    for field in ('first_created', 'version_created'):
        if value := document.get(field):
            try:
                date = datetime.fromisoformat(value.replace('Z', '+00:00'))
                if date.tzinfo is not None:
                    result['published_at'] = date.astimezone(timezone.utc).isoformat().replace('+00:00', 'Z')
                    break
            except ValueError:
                pass  # The original value remains in document and XML metadata.
    def identity(concept):
        return concept.get('uri') or concept.get('qcode') or next(iter(concept.get('names', [])), concept.get('value', ''))
    if providers := document.get('provider'):
        if source := identity(providers[0]):
            result['source'] = source
    for source, target in [('creators', 'author'), ('subjects', 'subjects')]:
        values = [identity(value) for value in document.get(source, [])]
        if values := list(dict.fromkeys(value for value in values if value)):
            result[target] = values
    if tags := document.get('keywords'):
        result['tags'] = list(dict.fromkeys(tags))
    places, countries = [], []
    for located in item.findall('contentMeta/located', {'': NAR}):
        concept = _concept(located)
        places.extend(concept.get('names') or [identity(concept)])
        for node in located.iter():
            code = node.attrib.get('qcode', '')
            if code.startswith('iso3166-1a2:'):
                countries.append(code.split(':', 1)[1])
    for values, field in [(places, 'place'), (countries, 'country')]:
        if values := list(dict.fromkeys(value for value in values if value)):
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


@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    source = invocation.request.input
    if source.size_bytes > invocation.configuration.get('max_input_bytes', 1048576):
        raise TerminalError('input_too_large', 'XML exceeds max_input_bytes; submit a smaller item')
    root = _parse(invocation.read_input())
    if root.tag == _tag('newsItem'):
        item = root
    elif root.tag == _tag('newsMessage'):
        items = root.findall('itemSet/newsItem', {'': NAR})
        if len(items) != 1 or len(root.findall('itemSet/*', {'': NAR})) != 1:
            raise TerminalError('item_count', 'Submit exactly one newsItem per Blob; split multi-item messages before submission')
        item = items[0]
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
    for name, role in [('headline', 'title'), ('slugline', 'body')]:
        for n, node in enumerate(item.findall(f'contentMeta/{name}', {'': NAR}), 1):
            if text := _text(node):
                candidates.append((f'{name}-{n}', role, text, contexts[node]))
    number = 0
    for inline in item.findall('contentSet/inlineXML', {'': NAR}):
        for node in inline.iter():
            if node.tag.rsplit('}', 1)[-1] == 'p' and (text := _text(node)):
                number += 1
                candidates.append((f'paragraph-{number}', 'body', text, contexts[node]))
    language = contexts[item].get('language')
    if not language:
        language_node = item.find('contentMeta/language', {'': NAR})
        language = language_node.attrib.get('tag') if language_node is not None else None
    if not language:
        language = next((context.get('language') for _, _, _, context in candidates
                         if context.get('language')), None)
    # A declared content language is also the fallback for untagged text Parts.
    if language:
        for _, _, _, context in candidates:
            context.setdefault('language', language)

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
        raise TerminalError('text_too_large', 'Text exceeds max_text_bytes; source metadata was not truncated')
    parts = [Part(key=key, role=role, content=TextContent(text=text),
                  extensions={'newsml-g2.text': _extension(context)} if context else None)
             for key, role, text, context in candidates]
    parts.append(Part(key='source', role='source',
                      content=BlobContent(blob_id=source.blob_id, media_type=source.media_type)))
    document = _metadata(item, language)
    extensions = {'newsml-g2.document': _extension(document),
                  'newsml-g2.metadata': _extension(_common_metadata(item, document)),
                  'newsml-g2.xml': _extension({'root': _tree(root)})}
    if paths := invocation.configuration.get('header_paths'):
        extensions['newsml-g2.headers'] = _extension(_selected_headers(root, paths))
    response = NormalizerResponse(manifest=ManifestContent(parts=parts), extensions=extensions,
                                  language=language or None,
                                  warnings=[ResponseWarning(code='paragraphs_grouped', message='Adjacent body paragraphs were grouped within 64 text Parts')] if merged else None)
    if len(json.dumps(response.to_dict(), ensure_ascii=False, separators=(',', ':')).encode()) > MAX_RESPONSE_BYTES:
        raise TerminalError('manifest_too_large', 'Normalized output exceeds 2 MiB; source metadata was not truncated')
    return response
