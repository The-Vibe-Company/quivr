"""The text notation of keyword queries and its translation to the JSON expression.

Run: python3 -m unittest discover -s tests
"""
import json
import subprocess
import sys
import unittest

from alerts.notation import NotationError, parse
from support import decision

T = lambda text: {"term": text}  # noqa: E731


def tree(query):
    expression = parse(query)
    assert expression["kind"] == "keywords", expression
    return expression["match"]


class Parse(unittest.TestCase):
    def test_words_phrases_and_fields(self):
        self.assertEqual(tree("grève"), T("grève"))
        self.assertEqual(tree('"Marine Le Pen"'), T("Marine Le Pen"))
        self.assertEqual(tree('author:"Jane Doe"'), {"field": "author", "equals": "Jane Doe"})
        self.assertEqual(tree("category:sport"), {"field": "category", "equals": "sport"})
        self.assertEqual(tree(r'"say \"cheese\""'), T('say "cheese"'))

    def test_urls_and_quoted_colons_are_text(self):
        self.assertEqual(tree("https://example.com/x"), T("https://example.com/x"))
        self.assertEqual(tree('"re:Invent"'), T("re:Invent"))

    def test_juxtaposed_words_are_all_required(self):
        self.assertEqual(tree("Marine Le Pen"), {"all": [T("Marine"), T("Le"), T("Pen")]})

    def test_not_binds_tighter_than_and_which_binds_tighter_than_or(self):
        self.assertEqual(tree("a OR b AND c"), {"any": [T("a"), {"all": [T("b"), T("c")]}]})
        self.assertEqual(tree("a AND b OR c"), {"any": [{"all": [T("a"), T("b")]}, T("c")]})
        self.assertEqual(tree("NOT a OR b"), {"any": [{"not": T("a")}, T("b")]})
        self.assertEqual(tree("a NOT b"), {"all": [T("a"), {"not": T("b")}]})

    def test_parentheses_group(self):
        self.assertEqual(tree("(a OR b) AND c"), {"all": [{"any": [T("a"), T("b")]}, T("c")]})
        self.assertEqual(tree("NOT (a OR b)"), {"not": {"any": [T("a"), T("b")]}})

    def test_same_operator_chains_flatten(self):
        self.assertEqual(tree("a AND (b AND c)"), {"all": [T("a"), T("b"), T("c")]})
        self.assertEqual(tree("(a OR b) OR c"), {"any": [T("a"), T("b"), T("c")]})

    def test_lowercase_operators_are_words(self):
        self.assertEqual(tree("rock and roll"), {"all": [T("rock"), T("and"), T("roll")]})

    def test_the_ticket_example(self):
        self.assertEqual(tree('"Airbus" AND (grève OR strike) NOT sport'),
                         {"all": [T("Airbus"), {"any": [T("grève"), T("strike")]}, {"not": T("sport")}]})

    def test_parsed_queries_decide_as_written(self):
        # Precedence changes the decision: the article mentions Airbus and grève, not Boeing.
        self.assertEqual(decision(tree("Boeing AND Airbus OR grève")), "match")
        self.assertEqual(decision(tree("Boeing AND (Airbus OR grève)")), "no_match")

    def test_mistakes_are_explained(self):
        cases = {
            "": "empty",
            "a AND": "end of the query",
            "(a OR b": "closing parenthesis",
            "a OR b)": "unexpected ')'",
            '"unterminated': "closing quote",
            "-sport": "use NOT",
            "!!!": "no letters or digits",
            "author:": "a value",
            "OR a": "unexpected OR",
            "NOT NOT NOT NOT NOT NOT NOT a": "nest",
            "(" * 5000 + "a" + ")" * 5000: "nest",
        }
        for query, fragment in cases.items():
            with self.subTest(query=query):
                with self.assertRaises(NotationError) as caught:
                    parse(query)
                self.assertIn(fragment, str(caught.exception))

    def test_deep_but_bounded_nesting_parses(self):
        # Six group levels, the most the expression schema accepts; parentheses around one item add none.
        self.assertEqual(tree("NOT (a OR (b AND NOT (c OR (d AND e))))"),
                         {"not": {"any": [T("a"), {"all": [T("b"), {"not": {"any": [T("c"), {"all": [T("d"), T("e")]}]}}]}]}})
        self.assertEqual(tree("((((a))))"), T("a"))


class Command(unittest.TestCase):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, "-m", "alerts.notation", *args], capture_output=True, text=True)

    def test_prints_the_json_expression(self):
        result = self.run_cli("grève OR strike")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"kind": "keywords", "match": {"any": [T("grève"), T("strike")]}})

    def test_reports_a_mistake(self):
        result = self.run_cli("(grève")
        self.assertEqual(result.returncode, 1)
        self.assertIn("closing parenthesis", result.stderr)


if __name__ == "__main__":
    unittest.main()
