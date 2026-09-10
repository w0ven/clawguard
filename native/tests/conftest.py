"""Prevent LiteLLM's development dotenv loader from importing deployment state."""
import os
os.environ.setdefault("LITELLM_MODE","PRODUCTION")
os.environ.setdefault("LITELLM_LOCAL_MODEL_COST_MAP","True")
