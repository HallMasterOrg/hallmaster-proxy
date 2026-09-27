// Stress-harness TLS front: terminates TLS on :3443 with a throwaway
// self-signed cert and pipes plaintext to the mock on :3000. It exists
// because @robojs/server is HTTP-only while the proxy always dials
// upstream over TLS. The stress build of the proxy skips upstream cert
// verification, so the cert's contents don't matter.
import net from "node:net";
import tls from "node:tls";
import selfsigned from "selfsigned";

const { private: key, cert } = await selfsigned.generate(
  [{ name: "commonName", value: "discord.com" }],
  { days: 1, algorithm: "sha256" },
);

tls
  .createServer({ key, cert }, (client) => {
    const upstream = net.connect(3000, "127.0.0.1");
    // Node sockets default to Nagle; gateway frames are small, so without
    // this each one can sit in the kernel waiting for the previous ACK.
    client.setNoDelay(true);
    upstream.setNoDelay(true);
    client.pipe(upstream).pipe(client);
    client.on("error", () => upstream.destroy());
    upstream.on("error", () => client.destroy());
  })
  .listen(3443, () => console.log("[tls-front] :3443 -> :3000"));
