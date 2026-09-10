import hashlib
import json
from pathlib import Path

import pytest

from clawguard_native.wiki import WikiStore, SITE

BUNDLE = json.loads((Path(__file__).parents[1]/"knowledge/wiki-20260910.json").read_text())


@pytest.fixture
def wiki(tmp_path):
    store = WikiStore(tmp_path/"wiki.sqlite3")
    store.import_bundle(BUNDLE,group_ids=[-123,-456],batch="reviewed-20260910")
    return store


def test_all_28_whole_guides_and_idempotence(wiki):
    primary = [s for s in BUNDLE["sources"] if s["recommended_primary"]]
    assert len(primary)==28
    for group in (-123,-456):
        for source in primary:
            result = wiki.retrieve(group,url=source["canonical_url"])
            exact = next(d for d in result["documents"] if d["canonical_url"] == source["canonical_url"])
            assert exact["body_markdown"] == source["body_markdown"]
            assert hashlib.sha256(exact["body_markdown"].encode()).hexdigest() == exact["body_sha256"]
            assert exact["complete"] and result["is_live"] is False
    again = wiki.import_bundle(BUNDLE,group_ids=[-123,-456],batch="reviewed-20260910")
    assert again["new_versions"]==again["new_grants"]==0
    with wiki.connect() as db:
        assert db.execute("SELECT count(*) FROM wiki_versions").fetchone()[0]==34
        assert db.execute("SELECT count(*) FROM wiki_grants").fetchone()[0]==68
        assert not any("expires" in row[1] for row in db.execute("PRAGMA table_info(wiki_grants)"))


@pytest.mark.parametrize("query,product,suffix",[
    ("Po0 出口 转发 准备","po0","/guide/tutorials/before-onboarding"),
    ("nftables 手动 转发","po0","/guide/tutorials/nftables-port-forwarding"),
    ("iOS 蜂窝 DNS","5gpn","/guide/5gpn/ios"),
    ("Android 私人 DNS","5gpn","/guide/5gpn/android"),
    ("5gpn 准备 前提","5gpn","/guide/5gpn/prerequisites"),
    ("5gpn 连通 诊断","5gpn","/guide/5gpn/faq"),
])
def test_queries_have_correct_product_source_and_complete_steps(wiki,query,product,suffix):
    for group in (-123,-456):
        result = wiki.retrieve(group,query=query,product=product,limit=3)
        assert SITE+suffix in [d["canonical_url"] for d in result["documents"]]
        assert all(d["product"].lower().startswith(product) for d in result["documents"])
        if product=="5gpn":
            pre = next(d for d in result["documents"] if d["canonical_url"].endswith("/prerequisites"))
            assert "浙江联通" in pre["body_markdown"] and "KFCHOST" in pre["body_markdown"]


def test_nftables_heredocs_danger_context_never_split(wiki):
    source = next(s for s in BUNDLE["sources"] if s["canonical_url"].endswith("/nftables-port-forwarding"))
    doc = wiki.retrieve(-123,url=source["canonical_url"])["documents"][0]
    assert doc["body_markdown"]==source["body_markdown"]
    for needle in ("flush ruleset","RELAY_LAN_IP","nft -c","EOF"):
        assert needle in doc["body_markdown"]


def test_acl_unknown_product_and_live_claims(wiki):
    assert wiki.retrieve(-999,query="nftables")["documents"]==[]
    assert wiki.retrieve(-123,query="5gpn iOS",product="po0")["error"]=="product_mismatch"
    assert wiki.retrieve(-123,query="量子计算机纠错算法")["unknown"]
    live = wiki.retrieve(-123,query="现在价格库存状态",product="po0")
    assert live["is_live"] is False
    assert "价格/库存" in live["usage"]
    assert live["live_entry_points"]==[SITE+"/status",SITE+"/looking-glass"]
    migrated = wiki.retrieve(-123,url=SITE+"/guide/tutorials/status-and-looking-glass")
    assert migrated["documents"][0]["canonical_url"]==SITE+"/guide/buying/status-and-looking-glass"


def test_revoke_one_batch_never_resurrects_or_affects_other_group(wiki):
    assert wiki.revoke_batch("reviewed-20260910",[-123])==34
    assert wiki.retrieve(-123,query="iOS")["documents"]==[]
    assert wiki.retrieve(-456,query="iOS")["documents"]
    wiki.import_bundle(BUNDLE,group_ids=[-123,-456],batch="reviewed-20260910")
    assert wiki.retrieve(-123,query="iOS")["documents"]==[]
    tampered = json.loads(json.dumps(BUNDLE))
    tampered["sources"][0]["body_markdown"]+="伪造"
    with pytest.raises(ValueError,match="hash"):
        wiki.import_bundle(tampered,group_ids=[-123],batch="bad")
