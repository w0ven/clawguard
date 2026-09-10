"""Source-validated assistant settings; CG still owns model/TG/TTS connections."""
from __future__ import annotations

import json
import os
import sqlite3
from pathlib import Path

from bot.services.runtime_config import (BotBehaviorConfig, PromptSettingsConfig, RuntimeConfig,
    LoggingSettingsConfig,MusicSettingsConfig,MovieInfoSettingsConfig,StickerSettingsConfig,SecretCipher)
from bot.utils.prompts import set_runtime_prompts
from bot.utils.logging_setup import configure_logging

PROMPTS = ("decision","casual","manage_intent","compress","skill_tools","sticker_decision",
           "reply_mode","persona","proactive_topic","style_distill")
LOG_LEVELS = ("DEBUG","INFO","WARNING","ERROR","CRITICAL")
SOURCE_UNWIRED = {"proactive_retry_minutes":"源82c3703仅postpone_cooldown_task读取，无生产调用者；不承诺失败重试。"}
EXCLUDED_BOT = {"drop_pending_updates","auto_delete_minutes"}
CATEGORIES = {"reply","media","proactive"}
MOVIE_SECRETS = {"tmdb_read_access_token","imdb_api_key","imdb_aws_access_key_id","imdb_aws_secret_access_key","imdb_aws_session_token"}
EXTRA_TYPES = {"music":MusicSettingsConfig,"movie_info":MovieInfoSettingsConfig,"stickers":StickerSettingsConfig}


class Configuration:
    def __init__(self,path: Path):
        self.path = path
        path.parent.mkdir(parents=True,exist_ok=True)
        with sqlite3.connect(path) as db:
            db.execute("CREATE TABLE IF NOT EXISTS cg_runtime (id INTEGER PRIMARY KEY, revision INTEGER NOT NULL, payload TEXT NOT NULL)")
            default = {"bot":{k:v for k,v in BotBehaviorConfig().model_dump().items() if k not in EXCLUDED_BOT},
                       "prompts":{k:v for k,v in PromptSettingsConfig.defaults().model_dump().items() if k in PROMPTS}}
            default["bot"]["auto_delete_categories"] = []
            default.update({key:model().model_dump() for key,model in EXTRA_TYPES.items()})
            default["logging"]=LoggingSettingsConfig().model_dump()
            db.execute("INSERT OR IGNORE INTO cg_runtime VALUES (1,1,?)",(json.dumps(default,ensure_ascii=False),))
        path.chmod(0o600)

    def _read(self):
        with sqlite3.connect(self.path) as db:
            revision,raw=db.execute("SELECT revision,payload FROM cg_runtime WHERE id=1").fetchone()
        value=json.loads(raw)
        for key,model in EXTRA_TYPES.items():value.setdefault(key,model().model_dump())
        value.setdefault("logging",LoggingSettingsConfig().model_dump())
        return revision,value

    def read(self):
        revision,value=self._read()
        encrypted=value.pop("_movie_secrets",{})
        # Secret inputs are write-only. Blank means keep; explicit clear list
        # is required to remove. Neither plaintext nor ciphertext leaves here.
        for key in MOVIE_SECRETS:value["movie_info"][key]=""
        return {**value,"revision":revision,"source_unwired":SOURCE_UNWIRED,
                "schema":BotBehaviorConfig.model_json_schema(),"prompt_keys":PROMPTS,
                "extra_schema":{key:model.model_json_schema() for key,model in EXTRA_TYPES.items()},
                "movie_secret_fields":sorted(MOVIE_SECRETS),"movie_secrets_configured":{key:bool(encrypted.get(key)) for key in MOVIE_SECRETS},
                "secret_storage_ready":SecretCipher(os.environ.get("CONFIG_MASTER_KEY","")).configured,
                "engine":"native","automatic_fact_learning":False,
                "log_levels":list(LOG_LEVELS)}

    def write(self,payload: dict):
        if set(payload)-{"bot","prompts","revision","music","movie_info","stickers","logging","clear_movie_secrets"}:
            raise ValueError("unsupported configuration namespace")
        _,old=self._read()
        bot_values={**old["bot"],**payload.get("bot",{})}
        if set(payload.get("bot",{})) & EXCLUDED_BOT:raise ValueError("CG owns update ingestion")
        for key in ("auto_delete_categories","auto_delete_category_seconds","auto_delete_category_mode"):
            if set(bot_values.get(key,[]))-CATEGORIES:raise ValueError("native assistant cannot configure CG moderation categories")
        bot=BotBehaviorConfig.model_validate(bot_values)
        if set(payload.get("prompts",{}))-set(PROMPTS):raise ValueError("unsupported assistant prompt")
        prompts={**old["prompts"],**payload.get("prompts",{})}
        PromptSettingsConfig.model_validate(prompts)
        logging_values={**old["logging"],**payload.get("logging",{})}
        log_settings=LoggingSettingsConfig.model_validate(logging_values)
        value={"bot":{k:v for k,v in bot.model_dump().items() if k not in EXCLUDED_BOT},"prompts":prompts,
               "logging":log_settings.model_dump()}
        encrypted=dict(old.get("_movie_secrets",{}))
        clear=set(payload.get("clear_movie_secrets",[]))
        if clear-MOVIE_SECRETS:raise ValueError("unsupported movie secret")
        for key in clear:encrypted.pop(key,None)
        for key,model in EXTRA_TYPES.items():
            fields={**old[key],**payload.get(key,{})}
            checked=model.model_validate(fields).model_dump()
            if key=="movie_info":
                for secret in MOVIE_SECRETS:
                    if checked[secret]:encrypted[secret]=SecretCipher(os.environ.get("CONFIG_MASTER_KEY","")).encrypt(checked[secret])
                    checked[secret]=""
            value[key]=checked
        value["_movie_secrets"]=encrypted
        with sqlite3.connect(self.path) as db:
            changed=db.execute("UPDATE cg_runtime SET revision=revision+1,payload=? WHERE id=1 AND revision=?",
                (json.dumps(value,ensure_ascii=False),int(payload["revision"]))).rowcount
            if not changed:raise ValueError("configuration revision conflict; refresh before saving")
        return self.read()

    def apply(self,settings):
        _,value=self._read()
        configure_logging(force=True,config=LoggingSettingsConfig.model_validate(value["logging"]))
        bot_models={key:getattr(settings.bot,key) for key in ("main_model","decision_model","compress_model","vision_model","moderation_model","embed_model")}
        connections={key:getattr(settings,key) for key in settings.__class__.model_fields if key.startswith("doubao_tts_")}
        movie=dict(value["movie_info"])
        for key,encrypted in value.get("_movie_secrets",{}).items():
            movie[key]=SecretCipher(settings.config_master_key).decrypt(encrypted)
        runtime=RuntimeConfig(bot=BotBehaviorConfig.model_validate(value["bot"]),
            prompts=PromptSettingsConfig.model_validate(value["prompts"]),music=MusicSettingsConfig.model_validate(value["music"]),
            movie_info=MovieInfoSettingsConfig.model_validate(movie),stickers=StickerSettingsConfig.model_validate(value["stickers"]))
        runtime.apply_to_settings(settings)
        for key,model in bot_models.items():setattr(settings.bot,key,model)
        for key,connection_value in connections.items():setattr(settings,key,connection_value)
        settings.moderation.enabled=False
        set_runtime_prompts(value["prompts"])
