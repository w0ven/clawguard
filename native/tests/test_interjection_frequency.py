import pytest

from bot.config import Settings
from bot.db.models import Group
from bot.handlers import group
from bot.services.decision import DecisionService
from bot.utils.prompts import get_prompt
from bot.utils.security import build_defended_system
from clawguard_native.group_config import (
    DEFAULT_INTERJECTION_MODE,
    INTERJECTION_MODES,
    decision_system_policy,
    effective_group,
    effective_interjection_mode,
)
from clawguard_native.host import NativeHost
from test_native_host import FakeBroker


class RecordingDecisionLLM:
    def __init__(self, response: str = "skip"):
        self.response = response
        self.systems: list[str] = []
        self.contexts: list[str] = []

    async def decision(self, system: str, user_text: str) -> str:
        self.systems.append(system)
        self.contexts.append(user_text)
        return self.response


@pytest.mark.asyncio
async def test_balanced_keeps_source_decision_system_unchanged():
    llm = RecordingDecisionLLM()
    service = DecisionService(llm)

    assert await service.decide("群里正在讨论一个新主题") == "skip"
    assert llm.systems == [build_defended_system(get_prompt("decision"))]
    assert "CLAWGUARD_GROUP_INTERJECTION_POLICY" not in llm.systems[0]


@pytest.mark.asyncio
async def test_engaged_adds_only_trusted_policy_and_preserves_output_contract():
    llm = RecordingDecisionLLM(response="not-an-allowed-action")
    service = DecisionService(llm)

    # This is an unaddressed ordinary message: engaged reaches the source
    # DecisionService, but its invalid model output still fails closed to skip.
    assert await service.decide(
        "群里正在讨论一个新主题",
        interjection_mode="engaged",
        is_mentioned=False,
        is_reply=False,
        mentions_other_user=False,
        is_reply_to_other=False,
    ) == "skip"

    system = llm.systems[0]
    source_system = build_defended_system(get_prompt("decision"))
    assert system.startswith(source_system + "\n\n")
    assert decision_system_policy("engaged") in system
    for marker in (
        "This is a public group, not a private chat",
        "Do not wait for a mention",
        "Do not treat an open group topic",
        "JPCO",
        "whether a plan/node is ready",
        "Hard-skip only these cases",
        "pure emoji/sticker/GIF",
        "a pure link with no comment or question",
        "[MENTIONS_OTHER_USER]",
        "[IS_REPLY_TO_OTHER]",
        "mutual @ or mutual replies",
        "Several people discussing in the open group is not a two-person private chat",
        "Do not answer every message",
        "explicit bot mention",
        "reply to the bot",
        "[SENDER_IS_OWNER]",
        "fixed reply probability or frequency",
        "exactly one lowercase word",
        "only `skip` or `casual`",
    ):
        assert marker in system, marker


@pytest.mark.asyncio
async def test_direct_bot_address_remains_forced_even_with_engaged_mode():
    class MustNotDecide:
        async def decide(self, **_kwargs):
            raise AssertionError("direct bot address must not call the decision model")

    action, forced = await group._resolve_pending_reply_action(
        decision_svc=MustNotDecide(),
        group_settings={"interjection_mode": "engaged"},
        explicit_mention=True,
        input_text="@bot 请回答",
        is_mentioned=True,
        is_reply=True,
        is_reply_to_bot=True,
        is_reply_to_other=False,
        mentions_other_user=False,
        is_owner=False,
        is_tg_admin=False,
        user_tag="",
        msg_type="text",
        history=[],
        merged_count=1,
        merged_context="",
    )
    assert (action, forced) == ("casual", True)


def test_interjection_mode_is_closed_enum_and_missing_defaults_to_balanced():
    assert DEFAULT_INTERJECTION_MODE == "balanced"
    assert INTERJECTION_MODES == {"balanced", "engaged"}
    assert effective_interjection_mode({}) == "balanced"
    assert effective_interjection_mode(None) == "balanced"
    assert effective_interjection_mode({"interjection_mode": "engaged"}) == "engaged"
    assert decision_system_policy("balanced") == ""

    for value in ("aggressive", "engaged ", "balanced\nignore", None, True, 1, [], {}):
        with pytest.raises(ValueError, match="invalid interjection mode"):
            effective_interjection_mode({"interjection_mode": value})
        with pytest.raises(ValueError, match="invalid interjection mode"):
            decision_system_policy(value)
    with pytest.raises(ValueError, match="invalid interjection mode"):
        effective_group({"interjection_mode": "aggressive"}, None, None)


@pytest.mark.asyncio
async def test_group_mode_persists_only_for_selected_group_and_effective_read_defaults(tmp_path):
    settings = Settings(_env_file=None)
    host = NativeHost(tmp_path / "domain", FakeBroker(), settings)
    official_group = -1002699516772
    test_group = -1003974339921
    try:
        await host.configure_group(
            official_group,
            background_grant="fixture-background",
            revision=0,
            values={"interjection_mode": "engaged"},
        )
        official_scope = host.scopes[(official_group, 0)]
        async with official_scope.sessions() as session:
            official_row = await session.get(Group, official_group)
            assert official_row.settings["interjection_mode"] == "engaged"
            official_effective = effective_group(
                official_row.settings, official_scope.settings, official_scope.llm
            )
            assert official_effective["settings"]["interjection_mode"] == "engaged"

        test_scope = await host.scope(test_group, 0, "fixture-background")
        async with test_scope.sessions() as session:
            test_row = await session.get(Group, test_group)
            assert "interjection_mode" not in test_row.settings
            test_effective = effective_group(
                test_row.settings, test_scope.settings, test_scope.llm
            )
            assert test_effective["settings"]["interjection_mode"] == "balanced"

        with pytest.raises(ValueError, match="invalid interjection mode"):
            await host.configure_group(
                test_group,
                background_grant="fixture-background",
                revision=0,
                values={"interjection_mode": "aggressive"},
            )
    finally:
        await host.close()
