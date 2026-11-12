"""A small allowlisted public-document catalog, not arbitrary URL or filesystem access."""

import httpx
import time

from .protocol import Query, Read, Report, digest

PUBLIC_REVISION = "dd159bc2a9a6fb1b0f8720aaa115ca1f7f263959"
CATALOG = {"readme": "README.md", "workflows": "docs/project-workflows.md"}
SCHEMAS = {"query_public": Query, "read_evidence": Read, "submit_report": Report}
TOOL_SCHEMAS = [
    {
        "type": "function",
        "function": {
            "name": name,
            "description": {
                "query_public": "Find relevant documents in the fixed public Chora catalog.",
                "read_evidence": "Read a catalog document by evidence_id.",
                "submit_report": "Submit a structured report supported by previously read evidence.",
            }[name],
            "parameters": schema.model_json_schema(),
            "strict": True,
        },
    }
    for name, schema in SCHEMAS.items()
]


class PublicCatalog:
    identity = {"revision": PUBLIC_REVISION, "catalog_digest": digest(CATALOG)}

    def fetch(self, evidence_id, timeout):
        if evidence_id not in CATALOG:
            raise ValueError("unknown evidence ID")
        url = f"https://raw.githubusercontent.com/Yangyang96/chora/{PUBLIC_REVISION}/{CATALOG[evidence_id]}"
        deadline = time.monotonic() + timeout
        with httpx.Client(timeout=timeout, follow_redirects=False, trust_env=False) as client:
            with client.stream("GET", url) as response:
                response.raise_for_status()
                body = bytearray()
                for chunk in response.iter_bytes():
                    if time.monotonic() >= deadline:
                        raise httpx.ReadTimeout("evidence deadline exceeded")
                    body.extend(chunk)
                    if len(body) > 100000:
                        raise ValueError("evidence exceeds limit")
        content = body.decode("utf-8")
        return {
            "evidence_id": evidence_id,
            "url": url,
            "content": content,
            "digest": digest(content),
        }

    def query(self, query, timeout):
        # Bounded lexical catalog search. Full RAG belongs to M2.
        terms = set(query.lower().split())
        hits = []
        for evidence_id in CATALOG:
            doc = self.fetch(evidence_id, timeout / len(CATALOG))
            score = sum(term in doc["content"].lower() for term in terms)
            if score:
                hits.append({"evidence_id": evidence_id, "url": doc["url"], "score": score})
        return sorted(hits, key=lambda hit: (-hit["score"], hit["evidence_id"]))


def validate_result(name, value):
    if name == "query_public":
        if (
            not isinstance(value, list)
            or len(value) > len(CATALOG)
            or any(
                not isinstance(hit, dict)
                or hit.get("evidence_id") not in CATALOG
                or not isinstance(hit.get("url"), str)
                or type(hit.get("score")) is not int
                for hit in value
            )
        ):
            raise ValueError("invalid query result")
    elif name == "read_evidence":
        if (
            not isinstance(value, dict)
            or value.get("evidence_id") not in CATALOG
            or not isinstance(value.get("content"), str)
            or len(value["content"].encode()) > 100000
            or not isinstance(value.get("url"), str)
            or value.get("digest") != digest(value["content"])
        ):
            raise ValueError("invalid evidence result")
    return value
