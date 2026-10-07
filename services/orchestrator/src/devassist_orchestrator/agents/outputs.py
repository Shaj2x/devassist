"""What each agent must return. These models double as the JSON schemas the
providers enforce (see llm.schema.strict_schema), so every field is required."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, Field

from devassist_orchestrator.tools.workspace import FileEdit


class PlannedFile(BaseModel):
    path: str
    change: str = Field(description="What changes in this file and why")


class Plan(BaseModel):
    summary: str = Field(description="One or two sentences: what will be done")
    files_to_change: list[PlannedFile] = Field(description="Existing or new source files to edit")
    steps: list[str] = Field(description="Ordered implementation steps")
    risks: list[str] = Field(description="What could go wrong or regress")
    test_strategy: str = Field(description="How the change will be tested")
    search_queries: list[str] = Field(
        description="Code searches that would help implement the plan"
    )


class CodeChange(BaseModel):
    edits: list[FileEdit] = Field(description="Complete new contents of every file changed")
    explanation: str = Field(description="Short explanation of the change for the reviewer")


class DebugResult(BaseModel):
    diagnosis: str = Field(description="Root cause of the validation failure")
    edits: list[FileEdit] = Field(
        description="Complete new contents of every file changed to fix it"
    )
    explanation: str


class Review(BaseModel):
    summary: str = Field(description="What the patch does, for a human reviewer")
    risk_level: Literal["low", "medium", "high"]
    concerns: list[str] = Field(description="Anything the human should check before merging")
    suggested_followups: list[str]
    ready_to_merge: bool
