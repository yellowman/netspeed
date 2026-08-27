#!/usr/bin/env python3
import argparse, http.server, json, socketserver, subprocess, threading, urllib.parse
class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args): pass
    def do_GET(self):
        u=urllib.parse.urlsplit(self.path)
        if u.path=='/meta':
            body=b'not a netspeed meta document';self.send_response(404);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);return
        if u.path=='/__down':
            n=int(urllib.parse.parse_qs(u.query).get('bytes',['0'])[0]);self.send_response(200);self.send_header('CF-Ray','fixture');self.send_header('Server-Timing','cfReqDur;dur=0.1');self.send_header('Content-Length',str(n));self.end_headers();block=b'0'*65536
            while n: take=min(n,len(block));self.wfile.write(block[:take]);n-=take
            return
        self.send_response(404);self.send_header('Content-Length','0');self.end_headers()
    def do_POST(self):
        u=urllib.parse.urlsplit(self.path);n=int(self.headers.get('Content-Length','0'));got=0
        while got<n:
            b=self.rfile.read(min(65536,n-got))
            if not b: break
            got+=len(b)
        if u.path!='/__up' or got!=n: self.send_response(400);self.send_header('Content-Length','0');self.end_headers();return
        body=b'ok';self.send_response(200);self.send_header('CF-Ray','fixture');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
def run(binary):
    srv=socketserver.ThreadingTCPServer(('127.0.0.1',0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start();base=f'http://127.0.0.1:{srv.server_address[1]}'
    try:
        for direction in ('--download-only','--upload-only'):
            p=subprocess.run([binary,'--provider','auto','--server',base,'--quick',direction,'--skip-packet-loss','--json'],text=True,capture_output=True,timeout=45)
            if p.returncode: raise SystemExit(f'{direction}: rc={p.returncode}\nstdout={p.stdout}\nstderr={p.stderr}')
            doc=json.loads(p.stdout);assert doc['provider']=='cloudflare' and doc['measurementContract']=='cloudflare-http-v1' and doc['packetTopology']=='turn-loopback',doc
    finally: srv.shutdown();srv.server_close()
if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('binary');run(ap.parse_args().binary)
