import json
import pytest
from clawguard_native.host import NativeHost
from test_native_host import FakeBroker


async def test_production_activation_checks_latest_before_marking_authority(tmp_path):
    path=tmp_path/'migration-manifest.json'
    path.write_text(json.dumps({'state':'prepared','production':True,'profiles':{'-123':{'legacy_policy_version':10}}}))
    class Broker(FakeBroker):
        allow=False
        async def call(self,operation,payload,*,scope=None):
            assert operation=='migration-check'
            assert payload['profiles']['-123']['legacy_policy_version']==10
            if not self.allow:raise RuntimeError('latest policy is now v11')
            return True
    broker=Broker();host=NativeHost(tmp_path,broker)
    try:
        assert host.activation_required
        with pytest.raises(RuntimeError,match='verification'):
            await host.scope(-123,0,'test')
        with pytest.raises(RuntimeError,match='v11'):
            await host.activate()
        assert 'activated_at' not in json.loads(path.read_text())
        broker.allow=True
        await host.activate()
        assert json.loads(path.read_text())['activated_at']
        assert not host.activation_required
    finally:await host.close()
