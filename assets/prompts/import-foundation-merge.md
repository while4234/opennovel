You are reconstructing a source novel foundation from compact per-chapter fact
reports. You must not ask for or rely on the original prose. Treat the reports
as the only source of truth.

Goal:
- Produce a durable foundation for later adaptation planning.
- Merge recurring character, relationship, setting, and conflict facts.
- Preserve causal order and unresolved narrative pressure.
- Do not invent chapter bodies, scenes, quotations, or unsupported details.

Output exactly five tagged sections:

=== PREMISE ===
Markdown. Start with a single H1 title line. Summarize the core premise,
central conflict, protagonist pressure, tone, and source-book direction.

=== CHARACTERS ===
This is the formal adaptable cast, not a list of every person mentioned in the
reports. Include only protagonists, core characters, major antagonists, and
major supporting characters who keep changing the main causal line. Do not
create formal cards for one-scene occupations, walk-ons, crowd labels,
appearance-only labels, merely mentioned people, or titles whose independent
identity is not established. Merge an identity title with its later real name
as aliases when evidence supports that identity. When a personality, dream
identity, or mistaken identity is evidenced as the same person, keep one card
and record the internal conflict or knowledge boundary instead of splitting it.

JSON array of objects compatible with:
{
  "id": "stable source character ID; distinguish same-name people",
  "name": "string",
  "aliases": ["string"],
  "role": "string",
  "gender": "male|female|nonbinary|unspecified",
  "description": "string",
  "arc": "changes already evidenced in the reports; never invent a future endpoint",
  "traits": ["string"],
  "tier": "core|important|secondary|decorative",
  "faction": "string",
  "goal": "string",
  "motivation": "string",
  "conflict": "string",
  "voice": "string",
  "constraints": ["string"],
  "contrast_details": [{"surface":"string","depth":"string"}],
  "key_backstory": [{"event":"string","impact":"string"}],
  "initial_state": {
    "identity":"string","situation":"string","emotion":"string",
    "resources":["string"],"relationships":"string"
  },
  "knowledge_boundary": {
    "known":["string"],"unknown":["string"],
    "misconceptions":["string"],"forbidden":["string"]
  },
  "notes": "uncertainty or evidence boundary"
}

=== RELATIONSHIPS ===
JSON array of source-novel relationship objects compatible with:
{
  "id": "stable relationship ID",
  "source_character_id": "an ID from CHARACTERS",
  "target_character_id": "a different ID from CHARACTERS",
  "type": "ally|rival|family|romantic|mentor|professional|other",
  "label": "short source relationship label",
  "direction": "directed|bidirectional|undirected",
  "status": "active|strained|broken|resolved",
  "description": "relationship facts and changes evidenced by the reports",
  "since": "earliest evidenced source chapter or stage",
  "tags": ["string"],
  "constraints": ["evidence boundary or continuity constraint"]
}

=== WORLD_RULES ===
JSON array of objects compatible with:
{
  "category": "magic|technology|geography|society|other",
  "rule": "string",
  "boundary": "string"
}

=== COMPASS ===
JSON object compatible with:
{
  "ending_direction": "string",
  "open_threads": ["string"],
  "estimated_scale": "short|mid|long",
  "last_updated": 0
}

Rules:
- Return only the tagged sections above.
- Do not output LAYERED_OUTLINE. It is generated deterministically by code.
- Use only the Character fields listed above. Never emit legacy `goals` or
  top-level character `relationships`; relationship evidence belongs in the
  separate RELATIONSHIPS section.
- Every final character must have a `gender`. Use only explicit source evidence
  such as self-description, sex/kinship terms, or attributable pronouns. Never
  infer gender from a name, occupation, personality, or stereotype. Use
  `unspecified` when the reports do not establish it and add a constraint to
  keep later references on the name/title instead of inventing a pronoun.
- Preserve evidenced `contrast_details` and `key_backstory` for core and
  important characters. A backstory item must state both the past event and its
  evidenced present impact. A contrast must state observable surface behavior
  and the less-visible evidenced motive or behavior.
- Choose tiers by evidence and narrative load. Core and important characters
  should receive the fullest report-supported profile; do not promote a thinly
  evidenced walk-on merely to make the schema look complete.
- A batch-local `major_characters` signal is evidence about that source range,
  not whole-book permission to create a formal card.
- RELATIONSHIPS contains source facts, not adaptation plans. Keep only
  relationships with two stable character IDs and report evidence. Do not
  invent a relationship to connect isolated characters.
- Merge aliases into one character only when report evidence supports the
  identity. Keep same-name different people separate with stable IDs. Preserve
  renames as aliases and preserve chapter ranges in compact notes when useful.
- Leave unsupported fields empty and mark uncertainty in notes. Never invent a
  complete growth endpoint not present in the reports.
- Use concise but specific facts from the reports.
- Keep all JSON valid. No trailing commas, comments, or markdown fences around JSON.
