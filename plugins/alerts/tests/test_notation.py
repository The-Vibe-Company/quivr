"""The text notation of keyword queries and its translation to the JSON expression.

Run: python3 -m unittest discover -s tests
"""
import json
import pathlib
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


class SharedCases(unittest.TestCase):
    """tests/notation-cases.json is also asserted by the demo's TypeScript port
    (quivr-search/tests/notation.spec.ts), so both parsers read queries the same way."""

    def test_the_shared_cases(self):
        cases = json.loads((pathlib.Path(__file__).parent / "notation-cases.json").read_text())
        for case in cases["valid"]:
            with self.subTest(query=case["query"]):
                self.assertEqual(tree(case["query"]), case["match"])
        for query in cases["invalid"]:
            with self.subTest(query=query[:40]):
                with self.assertRaises(NotationError):
                    parse(query)


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
