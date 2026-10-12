# SPDX-License-Identifier: GPL-3.0-or-later
"""Validate actual Go-serialized fixtures. No network or schema fetch is used."""
import json
import sys
from jsonschema import Draft202012Validator, FormatChecker

with open(sys.argv[1], encoding="utf-8") as source:
    schema = json.load(source)
Draft202012Validator.check_schema(schema)
Draft202012Validator(schema, format_checker=FormatChecker()).validate(json.load(sys.stdin))
