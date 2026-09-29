"""Conversion between generated dataclass models and JSON-compatible values.

Decoding checks shapes and types; full JSON Schema validation (patterns, bounds)
is done separately by quivr_plugin.schema against the contract schemas.
"""
from __future__ import annotations

import dataclasses
import types
import typing
from typing import Any, Literal, Self, Union


class Model:
    """Base class of every generated Plugin Protocol model."""

    @classmethod
    def from_dict(cls, data: Any) -> Self:
        """Decode a JSON object into this model; raises ValueError on a shape mismatch."""
        return _decode(cls, data, cls.__name__)

    def to_dict(self) -> dict[str, Any]:
        """Encode as a JSON-compatible dict; unset optional fields are omitted."""
        return _encode(self)


_hints: dict[type, dict[str, Any]] = {}


def _type_hints(cls: type) -> dict[str, Any]:
    if cls not in _hints:
        _hints[cls] = typing.get_type_hints(cls)
    return _hints[cls]


def _encode(value: Any) -> Any:
    if isinstance(value, Model):
        out = {}
        for field in dataclasses.fields(value):
            item = getattr(value, field.name)
            # Unset optional fields are omitted; a required field keeps null.
            if item is not None or field.default is dataclasses.MISSING:
                out[field.name] = _encode(item)
        return out
    if isinstance(value, dict):
        return {key: _encode(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [_encode(item) for item in value]
    return value


def _decode(tp: Any, data: Any, path: str) -> Any:
    if tp is Any:
        return data
    origin = typing.get_origin(tp)
    if origin in (Union, types.UnionType):
        options = typing.get_args(tp)
        if data is None and type(None) in options:
            return None
        options = [o for o in options if o is not type(None)]
        if len(options) == 1:
            return _decode(options[0], data, path)
        return _decode_union(options, data, path)
    if origin is Literal:
        if data not in typing.get_args(tp):
            raise ValueError(f"{path}: expected one of {list(typing.get_args(tp))}, got {data!r}")
        return data
    if origin is list:
        if not isinstance(data, list):
            raise ValueError(f"{path}: expected an array")
        (item,) = typing.get_args(tp)
        return [_decode(item, value, f"{path}[{i}]") for i, value in enumerate(data)]
    if origin is dict:
        if not isinstance(data, dict):
            raise ValueError(f"{path}: expected an object")
        _, item = typing.get_args(tp)
        return {key: _decode(item, value, f"{path}.{key}") for key, value in data.items()}
    if isinstance(tp, type) and issubclass(tp, Model):
        return _decode_model(tp, data, path)
    if tp is bool:
        if not isinstance(data, bool):
            raise ValueError(f"{path}: expected a boolean")
        return data
    if tp is int:
        if isinstance(data, bool) or not isinstance(data, int):
            raise ValueError(f"{path}: expected an integer")
        return data
    if tp is float:
        if isinstance(data, bool) or not isinstance(data, (int, float)):
            raise ValueError(f"{path}: expected a number")
        return data
    if tp is str:
        if not isinstance(data, str):
            raise ValueError(f"{path}: expected a string")
        return data
    raise TypeError(f"{path}: unsupported annotation {tp!r}")


def _decode_model(cls: type[Model], data: Any, path: str) -> Model:
    if not isinstance(data, dict):
        raise ValueError(f"{path}: expected an object")
    hints = _type_hints(cls)
    names = {field.name for field in dataclasses.fields(cls)}
    unknown = sorted(set(data) - names)
    if unknown:
        raise ValueError(f"{path}: unknown field(s) {', '.join(unknown)}")
    values = {}
    for field in dataclasses.fields(cls):
        if field.name in data:
            values[field.name] = _decode(hints[field.name], data[field.name], f"{path}.{field.name}")
        elif field.default is dataclasses.MISSING:
            raise ValueError(f"{path}: missing required field {field.name}")
    return cls(**values)


def _decode_union(options: list[Any], data: Any, path: str) -> Any:
    # Protocol unions are discriminated by a Literal "kind" field.
    if isinstance(data, dict):
        for option in options:
            if isinstance(option, type) and issubclass(option, Model):
                kind = _type_hints(option).get("kind")
                if kind is not None and data.get("kind") in typing.get_args(kind):
                    return _decode_model(option, data, path)
    raise ValueError(f"{path}: value matches none of {[getattr(o, '__name__', o) for o in options]}")
