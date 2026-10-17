# SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
# SPDX-License-Identifier: MIT
"""Render docs/api/openapi.yaml as the API reference.

sphinxcontrib-openapi's ``httpdomain`` renderer draws each operation; this
renderer adds what the reference needs from the spec that the stock one
leaves out, so that every fact stays in the spec alone:

* one section per tag, in the spec's tag order, with the tag's
  description (an operation belongs to exactly one tag);
* each operation's ``operationId`` and its access, stated from its
  ``security`` requirements (scripts/openapi_routes_test.go checks those
  against the handlers, with the same alternative rule as _security_key);
* map values (a schema-valued ``additionalProperties``), drawn as one
  ``{key}`` property named by ``propertyNames.title``;
* a "Shared responses" section that draws each response of
  ``components/responses`` once, with its headers and every ``oneOf``
  variant of its body; operations link to it in one line.

Loaded as an extension from conf.py: it replaces the ``openapi`` directive
with one drawn by this renderer, so docs/api/reference.rst uses the plain
directive. (Registering the class through the ``openapi_renderers`` config
value would put a class in the pickled config, which Sphinx warns about.)
"""

import copy

from sphinxcontrib.openapi import utils
from sphinxcontrib.openapi.directive import create_directive_from_renderer
from sphinxcontrib.openapi.renderers import HttpdomainRenderer

_METHODS = ("get", "put", "post", "delete", "patch", "head", "options", "trace")

# The access classes of docs/api/conventions.md, keyed by _security_key.
_ACCESS = {
    (): "public: no token is needed.",
    ("sessionToken", "apiToken", "-"): (
        "a session or an API token; anonymous callers too while "
        "``api.require_auth`` is off or from an exempt network."
    ),
    ("sessionToken", "apiToken"): "a signed-in session or an API token.",
    ("sessionToken",): "a signed-in session; an API token is refused.",
    ("sessionToken:admin",): "an administrator's signed-in session.",
}

# Marks a shared response's one-line form until render_operation turns it
# into a cross-reference (the description passes through the Markdown
# converter, which would escape a role written directly).
_SHARED_MARK = "AVELOXISSHAREDRESPONSE"


def _label(name):
    return "shared-response-" + name.lower()


def _security_key(security):
    """One entry per alternative (OR); the schemes of one alternative (AND),
    each with its roles, sorted and joined by "+"; "-" for {}."""
    key = []
    for alternative in security:
        if not alternative:
            key.append("-")
            continue
        parts = sorted(scheme + (":" + ",".join(roles) if roles else "") for scheme, roles in alternative.items())
        key.append("+".join(parts))
    return tuple(key)


def _structural_type_lists(node):
    """Rewrite ``type: [object|array, "null"]`` for the stock renderer.

    The httpdomain renderer reads ``type`` as one string when it decides
    whether to draw an object's properties or an array's items (a list
    raises at the root and hides the structure below it). The structure is
    kept and the null alternative goes into the description, so the page
    still says it. Scalar type lists (``[string, "null"]``) render as they
    are.
    """
    if isinstance(node, dict):
        t = node.get("type")
        if isinstance(t, list):
            structural = [x for x in t if x in ("object", "array")]
            if len(structural) == 1:
                node["type"] = structural[0]
                others = [x for x in t if x != structural[0]]
                if others:
                    note = "Or " + " or ".join("``%s``" % x for x in others) + "."
                    desc = node.get("description", "").rstrip()
                    node["description"] = (desc + " " + note).strip() if desc else note
        for value in list(node.values()):
            _structural_type_lists(value)
    elif isinstance(node, list):
        for value in node:
            _structural_type_lists(value)


def _maps_as_properties(node, seen=None):
    """Draw a schema-valued ``additionalProperties`` as one ``{key}``
    property: the stock renderer walks ``properties`` only, so a map's
    values (``/repos/stats``'s per-repository statistics) drew nothing."""
    seen = set() if seen is None else seen
    if id(node) in seen:
        return
    seen.add(id(node))
    if isinstance(node, dict):
        extra = node.get("additionalProperties")
        if isinstance(extra, dict) and extra:
            key = "{%s}" % ((node.get("propertyNames") or {}).get("title") or "key")
            props = node.setdefault("properties", {})
            if key not in props:
                value = dict(extra)
                if not value.get("description"):
                    value["description"] = "One entry per %s." % key
                props[key] = value
            node.setdefault("type", "object")
        for value in list(node.values()):
            _maps_as_properties(value, seen)
    elif isinstance(node, list):
        for value in node:
            _maps_as_properties(value, seen)


def _first_sentence(text):
    text = " ".join(text.split())
    end = text.find(". ")
    return text if end < 0 else text[: end + 1]


def _shared_responses_by_reference(spec):
    """Draw each shared response (a ``$ref`` to ``components/responses``)
    as one line: its first sentence and a link to its full entry under
    Shared responses. The spec keeps the full response for tools; the page
    would otherwise repeat the rate-limit headers on every entry."""
    shared = spec.get("components", {}).get("responses", {})
    for item in spec.get("paths", {}).values():
        for method in _METHODS:
            responses = (item.get(method) or {}).get("responses", {})
            for code, response in list(responses.items()):
                ref = response.get("$ref", "") if isinstance(response, dict) else ""
                name = ref.rsplit("/", 1)[-1]
                if ref.startswith("#/components/responses/") and name in shared:
                    responses[code] = {
                        "description": _first_sentence(shared[name].get("description", ""))
                        + " %s%s" % (_SHARED_MARK, name)
                    }


def _resolve(spec, node):
    """Follow local $refs; returns (node, name of the last schema followed)."""
    name = None
    for _ in range(10):
        if not (isinstance(node, dict) and "$ref" in node):
            break
        ref = node["$ref"]
        name = ref.rsplit("/", 1)[-1]
        cur = spec
        for part in ref[2:].split("/"):
            cur = cur[part]
        node = cur
    return node, name


class AveloxisRenderer(HttpdomainRenderer):
    """The httpdomain renderer, sectioned by tag, with access lines and the
    shared responses."""

    option_spec = dict(HttpdomainRenderer.option_spec)

    def render_restructuredtext_markup(self, spec):
        # The directive caches the parsed spec; normalizing mutates it.
        original = copy.deepcopy(spec)
        spec = copy.deepcopy(spec)
        _shared_responses_by_reference(spec)
        utils.normalize_spec(spec, **self._options)
        _structural_type_lists(spec.get("paths", {}))
        _maps_as_properties(spec.get("paths", {}))
        global_security = spec.get("security", [])

        sections = {tag["name"]: {} for tag in spec.get("tags", [])}
        for endpoint, item in spec.get("paths", {}).items():
            common = item.get("parameters", [])
            for method in _METHODS:
                if method not in item:
                    continue
                operation = item[method]
                operation.setdefault("security", global_security)
                tags = operation.get("tags") or ["Other"]
                bucket = sections.setdefault(tags[0], {})
                path = bucket.setdefault(endpoint, {"parameters": list(common)})
                path[method] = operation

        descriptions = {tag["name"]: tag.get("description", "") for tag in spec.get("tags", [])}
        for name, paths in sections.items():
            if not paths:
                continue
            yield name
            yield "-" * len(name)
            yield ""
            if descriptions.get(name):
                yield from self._convert_markup(descriptions[name]).strip().splitlines()
                yield ""
            yield from self.render_paths(paths)

        yield from self.render_shared_responses(original)

    # --- shared responses -------------------------------------------------

    def _inline(self, text):
        """One paragraph of Markdown as reStructuredText on one line."""
        return " ".join(self._convert_markup(" ".join(text.split())).split())

    def _type_text(self, spec, schema):
        schema, _ = _resolve(spec, schema)
        t = schema.get("type")
        if isinstance(t, list):
            t = " or ".join(t)
        if not t:
            t = "object" if "properties" in schema or "allOf" in schema else "any"
        if t == "array":
            item, name = _resolve(spec, schema.get("items", {}))
            t = "array of " + (name or self._type_text(spec, item))
        if schema.get("format"):
            t += ", " + schema["format"]
        return t

    def _merged(self, spec, schema):
        """allOf folded into one object (properties and required)."""
        schema, _ = _resolve(spec, schema)
        if "allOf" not in schema:
            return schema
        out = {"type": "object", "properties": {}, "required": []}
        for part in schema["allOf"]:
            part = self._merged(spec, part)
            out["properties"].update(part.get("properties", {}))
            out["required"] += part.get("required", [])
        return out

    def _schema_bullets(self, spec, schema, depth=0):
        pad = "  " * depth
        schema = self._merged(spec, schema)
        lines = []
        variants = schema.get("oneOf") or schema.get("anyOf")
        if variants:
            lines.append(pad + "One of:")
            lines.append("")
            for v in variants:
                body, name = _resolve(spec, v)
                lines.append(pad + "* ``%s``" % (name or self._type_text(spec, body)))
                inner = self._schema_bullets(spec, body, depth + 1)
                if inner:
                    lines.append("")
                    lines += inner
                    lines.append("")
            return lines
        required = set(schema.get("required", []))
        for key, prop in schema.get("properties", {}).items():
            body, _ = _resolve(spec, prop)
            bits = [self._type_text(spec, body)]
            if key in required:
                bits.append("required")
            text = pad + "* ``%s`` (%s)" % (key, ", ".join(bits))
            notes = []
            if "const" in body:
                notes.append("always ``%s``" % body["const"])
            if body.get("enum"):
                notes.append("one of " + ", ".join("``%s``" % v for v in body["enum"]))
            if body.get("description"):
                notes.append(self._inline(body["description"]))
            if notes:
                text += " — " + "; ".join(notes)
            lines.append(text)
            nested = self._merged(spec, body)
            if nested.get("type") == "array":
                nested = self._merged(spec, nested.get("items", {}))
            if nested.get("properties") or nested.get("oneOf"):
                lines.append("")
                lines += self._schema_bullets(spec, nested, depth + 1)
                lines.append("")
        return lines

    def render_shared_responses(self, spec):
        shared = spec.get("components", {}).get("responses", {})
        if not shared:
            return
        yield "Shared responses"
        yield "----------------"
        yield ""
        yield (
            "Responses many operations share, written once. An entry above "
            "that links here sends exactly this response; the "
            ":doc:`conventions <conventions>` say what each status code means "
            "across the API."
        )
        yield ""
        for name, response in shared.items():
            yield ".. _%s:" % _label(name)
            yield ""
            yield name
            yield "~" * len(name)
            yield ""
            yield from self._convert_markup(response.get("description", "")).strip().splitlines()
            yield ""
            headers = response.get("headers") or {}
            if headers:
                yield "Headers:"
                yield ""
                for hname, header in headers.items():
                    header, _ = _resolve(spec, header)
                    yield "* ``%s`` (%s) — %s" % (
                        hname,
                        self._type_text(spec, header.get("schema", {})),
                        self._inline(header.get("description", "")),
                    )
                yield ""
            for media, content in (response.get("content") or {}).items():
                schema, ref = _resolve(spec, content.get("schema", {}))
                title = "Body, ``%s``" % media + (" (``%s``)" % ref if ref else "")
                bullets = self._schema_bullets(spec, schema)
                if bullets:
                    yield title + ":"
                    yield ""
                    yield from bullets
                    yield ""
                else:
                    desc = schema.get("description") or ("``%s``" % self._type_text(spec, schema))
                    yield "%s: %s" % (title, self._inline(desc) if schema.get("description") else desc)
                    yield ""

    # --- operations -------------------------------------------------------

    def render_operation(self, endpoint, method, operation):
        lines = list(super().render_operation(endpoint, method, operation))
        access = _ACCESS.get(_security_key(operation.get("security", [])))
        if access is None:
            raise ValueError(
                "%s %s: security %r is not one of the documented access classes"
                % (method.upper(), endpoint, operation.get("security"))
            )
        extra = [
            "   :Operation: ``%s``" % operation.get("operationId", ""),
            "   :Access: %s" % access,
            "",
        ]
        # After the directive line and its blank line (and :deprecated:).
        at = 2 if lines[1] == "" else 3
        lines = lines[:at] + extra + lines[at:]
        out = []
        for line in lines:
            while _SHARED_MARK in line:
                start = line.index(_SHARED_MARK)
                end = start + len(_SHARED_MARK)
                while end < len(line) and line[end].isalnum():
                    end += 1
                name = line[start + len(_SHARED_MARK) : end]
                line = line[:start] + ":ref:`Shared response %s <%s>`." % (name, _label(name)) + line[end:]
            out.append(line)
        return out


def _register_directive(app, config):
    app.add_directive("openapi", create_directive_from_renderer(AveloxisRenderer), override=True)


def setup(app):
    app.setup_extension("sphinxcontrib.openapi")
    # After sphinxcontrib.openapi registers its own (default priority 500).
    app.connect("config-inited", _register_directive, priority=900)
    return {"parallel_read_safe": True, "parallel_write_safe": True}
