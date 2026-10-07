from __future__ import annotations

from devassist_orchestrator.config import Settings
from devassist_orchestrator.llm.anthropic_provider import AnthropicProvider
from devassist_orchestrator.llm.base import LLMProvider
from devassist_orchestrator.llm.mock_provider import MockProvider, load_script
from devassist_orchestrator.llm.openai_provider import OpenAIProvider
from devassist_orchestrator.llm.pricing import price_for


def create_provider(settings: Settings) -> LLMProvider:
    """Build the provider named by LLM_PROVIDER."""
    price = price_for(
        settings.model, settings.llm_price_input_per_mtok, settings.llm_price_output_per_mtok
    )
    if settings.llm_provider == "anthropic":
        return AnthropicProvider(
            settings.model,
            price=price,
            api_key=settings.anthropic_api_key,
            timeout_seconds=settings.llm_timeout_seconds,
            max_retries=settings.llm_max_retries,
            max_tokens=settings.llm_max_tokens,
            refusal_fallback=settings.llm_refusal_fallback,
        )
    if settings.llm_provider == "openai":
        if not settings.openai_api_key:
            raise ValueError("LLM_PROVIDER=openai requires OPENAI_API_KEY")
        return OpenAIProvider(
            settings.model,
            price=price,
            api_key=settings.openai_api_key,
            timeout_seconds=settings.llm_timeout_seconds,
            max_retries=settings.llm_max_retries,
            max_tokens=settings.llm_max_tokens,
        )
    if not settings.llm_mock_script:
        raise ValueError("LLM_PROVIDER=mock requires LLM_MOCK_SCRIPT (a script name or path)")
    return MockProvider(load_script(settings.llm_mock_script))
