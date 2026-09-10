"""Isolated TLS fake used only on a Docker --internal network."""
import json
import ssl
from http.server import BaseHTTPRequestHandler,HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length','0')))
        method=self.path.rsplit('/',1)[-1]
        if method=='getMe':result={'id':999,'is_bot':True,'first_name':'Rescue test','username':'rescue_test_bot'}
        elif method=='getWebhookInfo':result={'url':'https://rescue.invalid/webhook/local-webhook','has_custom_certificate':False,'pending_update_count':0}
        elif method in {'setWebhook','deleteWebhook','setMyCommands','setChatMenuButton','sendMessage'}:result=True
        else:
            print('UNEXPECTED_TELEGRAM_METHOD',method,flush=True)
            self.send_response(500);self.end_headers();return
        body=json.dumps({'ok':True,'result':result}).encode()
        print('FAKE_TELEGRAM',method,flush=True)
        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    def log_message(self,*args):pass
server=HTTPServer(('0.0.0.0',443),Handler)
context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain('/test/cert.pem','/test/key.pem')
server.socket=context.wrap_socket(server.socket,server_side=True)
print('FAKE_TELEGRAM_LISTEN',flush=True)
server.serve_forever()
