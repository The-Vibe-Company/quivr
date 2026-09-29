"""Keyword alerts decided through the plugin's subscription route, as the core calls it.

Run: python3 -m unittest discover -s tests
"""
import unittest
from pathlib import Path

from quivr_plugin.testing import expected_decisions, invoke_subscription_fixture

from alerts.rule import plugin
from support import decide, decision, reply

FIXTURES = Path(__file__).resolve().parent.parent / "fixtures"
T = lambda text: {"term": text}  # noqa: E731


class Terms(unittest.TestCase):
    def test_case_accents_and_punctuation_are_ignored(self):
        for term in ["greve", "GRÈVE", "Grève", "airbus", "l'intersyndicale", "intersyndicale d'Airbus"]:
            with self.subTest(term=term):
                self.assertEqual(decision(T(term)), "match")

    def test_words_match_whole(self):
        # "bus" is inside "Airbus" and "salarié" inside "salariés": neither is the whole word.
        for term in ["bus", "salarié", "Tou"]:
            with self.subTest(term=term):
                self.assertEqual(decision(T(term)), "no_match")

    def test_a_phrase_matches_consecutive_words_in_order(self):
        self.assertEqual(decision(T("appelle à la grève")), "match")
        self.assertEqual(decision(T("grève appelle")), "no_match")
        self.assertEqual(decision(T("salariés grève")), "no_match")

    def test_ligatures_and_compatibility_forms_fold(self):
        parts = [{"key": "body", "role": "body", "text": "Une œuvre ﬁnancée par l'Œuvre des Pupilles"}]
        self.assertEqual(decision(T("oeuvre financee"), parts=parts), "match")

    def test_only_title_and_body_are_searched_by_default(self):
        self.assertEqual(decision(T("manifestation")), "no_match")
        self.assertEqual(decision(T("manifestation"), configuration={"text_roles": ["caption"]}), "match")

    def test_a_term_without_letters_or_digits_never_matches(self):
        self.assertEqual(decision(T("%")), "no_match")


class Groups(unittest.TestCase):
    def test_all_any_not(self):
        cases = [
            ({"all": [T("Airbus"), T("grève")]}, "match"),
            ({"all": [T("Airbus"), T("Boeing")]}, "no_match"),
            ({"any": [T("Boeing"), T("grève")]}, "match"),
            ({"any": [T("Boeing"), T("Embraer")]}, "no_match"),
            ({"all": [T("Airbus"), {"not": T("sport")}]}, "match"),
            # "sport" appears only in the caption, which is not searched.
            ({"all": [T("Airbus"), {"not": T("syndicats")}]}, "no_match"),
            ({"not": T("Boeing")}, "match"),
            ({"all": [T("Airbus"), {"any": [T("strike"), T("grève")]}, {"not": T("sport")}]}, "match"),
        ]
        for match, want in cases:
            with self.subTest(match=match):
                self.assertEqual(decision(match), want)


class Fields(unittest.TestCase):
    EXTENSIONS = {"extensions": {"example.news": {"schema_version": "1", "data": {"author": "Jane Doé", "categories": ["Économie", "Social"]}}}}
    MAPPING = {"fields": {"author": "/extensions/example.news/data/author", "category": "/extensions/example.news/data/categories"}}

    def ask(self, match, **kwargs):
        return decision(match, record=self.EXTENSIONS, configuration=self.MAPPING, **kwargs)

    def test_built_in_fields_read_protocol_metadata(self):
        self.assertEqual(decision({"field": "source", "equals": "wire"}), "match")
        self.assertEqual(decision({"field": "source", "equals": "blogs"}), "no_match")
        self.assertEqual(decision({"field": "producer", "equals": "NEWSDESK"}), "match")
        connector = {"provenance": {"origin": "connector", "producer": "ci-1", "connector": {"instance_id": "ci-1", "kind": "rss"}}}
        self.assertEqual(decision({"field": "connector", "equals": "ci-1"}, record=connector), "match")
        self.assertEqual(decision({"field": "connector_kind", "equals": "rss"}, record=connector), "match")
        self.assertEqual(decision({"field": "connector", "equals": "ci-1"}), "no_match")

    def test_mapped_fields_compare_without_case_or_accents(self):
        self.assertEqual(self.ask({"field": "author", "equals": "jane doe"}), "match")
        self.assertEqual(self.ask({"field": "author", "equals": "Jane"}), "no_match")

    def test_a_list_field_matches_when_one_element_does(self):
        self.assertEqual(self.ask({"field": "category", "equals": "economie"}), "match")
        self.assertEqual(self.ask({"field": "category", "equals": "sport"}), "no_match")

    def test_an_unmapped_field_has_no_value(self):
        self.assertEqual(decision({"field": "author", "equals": "Jane Doé"}, record=self.EXTENSIONS), "no_match")

    def test_a_pointer_reads_metadata_without_a_mapping(self):
        self.assertEqual(decision({"field": "/extensions/example.news/data/author", "equals": "jane doe"}, record=self.EXTENSIONS), "match")

    def test_non_string_values_compare_as_json(self):
        record = {"extensions": {"example.news": {"schema_version": "1", "data": {"pages": 3, "breaking": True}}}}
        self.assertEqual(decision({"field": "/extensions/example.news/data/pages", "equals": 3}, record=record), "match")
        self.assertEqual(decision({"field": "/extensions/example.news/data/breaking", "equals": True}, record=record), "match")
        self.assertEqual(decision({"field": "/extensions/example.news/data/breaking", "equals": "yes"}, record=record), "no_match")

    def test_a_filter_combines_with_terms(self):
        self.assertEqual(self.ask({"all": [T("grève"), {"field": "author", "equals": "Jane Doe"}]}), "match")
        self.assertEqual(self.ask({"all": [T("grève"), {"not": {"field": "category", "equals": "social"}}]}), "no_match")


class Evidence(unittest.TestCase):
    def test_a_match_names_the_terms_and_the_parts_where_they_matched(self):
        answer = decide({"all": [T("Airbus"), {"any": [T("strike"), T("syndicats")]}, {"not": T("sport")}]})
        evidence = answer["evidence"]
        self.assertEqual(evidence["explanation"], 'Matched "Airbus" in title, body; "syndicats" in body.')
        self.assertEqual(evidence["part_keys"], ["title", "body"])
        self.assertEqual(evidence["details"], {"kind": "keywords", "terms": [
            {"term": "Airbus", "part_keys": ["title", "body"]}, {"term": "syndicats", "part_keys": ["body"]}], "fields": []})

    def test_a_filter_match_names_the_field_and_its_value(self):
        evidence = decide({"field": "source", "equals": "WIRE"})["evidence"]
        self.assertEqual(evidence["explanation"], 'Matched source "wire".')
        self.assertEqual(evidence["details"]["fields"], [{"field": "source", "value": "wire"}])
        self.assertNotIn("part_keys", evidence)

    def test_a_query_matched_only_by_exclusions_says_so(self):
        evidence = decide({"not": T("Boeing")})["evidence"]
        self.assertEqual(evidence["explanation"], "Matched: none of the excluded terms appear.")

    def test_evidence_stays_within_the_protocol_bounds(self):
        # 200 Parts with long keys and 64 matching phrases would overflow the details bound unless trimmed.
        parts = [{"key": f"part-{i:03d}-" + "k" * 100, "role": "body", "text": " ".join(f"mot{j}" for j in range(64))} for i in range(200)]
        answer = decide({"any": [T(f"mot{j}") for j in range(64)]}, parts=parts)
        self.assertEqual(answer["decision"], "match")
        self.assertLessEqual(len(answer["evidence"]["part_keys"]), 100)
        self.assertLessEqual(len(answer["evidence"]["explanation"]), 4096)


class Enrichment(unittest.TestCase):
    def test_wait_for_enrichment_answers_not_ready_until_enriched(self):
        self.assertEqual(decision(T("grève"), subscription={"wait_for_enrichment": True}, enriched=False), "not_ready")
        self.assertEqual(decision(T("grève"), subscription={"wait_for_enrichment": True}, enriched=True), "match")

    def test_keyword_alerts_do_not_wait_by_default(self):
        self.assertEqual(decision(T("grève"), enriched=False), "match")


class Batches(unittest.TestCase):
    def test_every_evaluation_of_a_batch_gets_its_own_decision(self):
        answer = reply([({"kind": "keywords", "match": T("grève")}, {}), ({"kind": "keywords", "match": T("Boeing")}, {}),
                        ({"kind": "keywords", "match": T("grève")}, {"wait_for_enrichment": True})], enriched=False)
        self.assertEqual([(d["id"], d["decision"]) for d in answer.body["decisions"]], [("e1", "match"), ("e2", "no_match"), ("e3", "not_ready")])


class Schema(unittest.TestCase):
    """The core validates saved searches against the declared schema; the plugin refuses the same values."""

    def refused(self, expression, configuration=None):
        answer = reply([(expression, configuration or {})])
        return answer.status, answer.body.get("code")

    def test_malformed_queries_are_refused(self):
        deep = T("x")
        for _ in range(7):
            deep = {"not": deep}
        for expression in [
            {"kind": "keywords"},
            {"kind": "described", "text": "a plain-language description"},
            {"kind": "keywords", "match": {"term": "  "}},
            {"kind": "keywords", "match": {"all": []}},
            {"kind": "keywords", "match": {"term": "a", "all": [T("b")]}},
            {"kind": "keywords", "match": {"field": "Author", "equals": "x"}},
            {"kind": "keywords", "match": {"field": "/record_id", "equals": "x"}},
            {"kind": "keywords", "match": deep},
        ]:
            with self.subTest(expression=expression):
                self.assertEqual(self.refused(expression), (400, "invalid_expression"))

    def test_six_nested_groups_are_accepted(self):
        deep = T("Boeing")
        for _ in range(6):
            deep = {"not": deep}
        self.assertEqual(decision(deep), "no_match")

    def test_unknown_subscription_settings_are_refused(self):
        self.assertEqual(self.refused({"kind": "keywords", "match": T("a")}, {"wait_for_enrichment": "yes"}), (400, "invalid_subscription_configuration"))


class Fixture(unittest.TestCase):
    def test_sample_fixture_gives_its_expected_decisions(self):
        path = FIXTURES / "sample.json"
        response = invoke_subscription_fixture(plugin, path)
        self.assertEqual({d.id: d.decision for d in response.decisions}, expected_decisions(path))


if __name__ == "__main__":
    unittest.main()
