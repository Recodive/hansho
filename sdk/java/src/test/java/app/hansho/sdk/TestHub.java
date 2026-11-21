package app.hansho.sdk;

import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.function.Consumer;
import java.util.function.Function;

/** A local fake hub: records every request and answers with whatever the handler says. */
final class TestHub implements AutoCloseable {
    static final String KEY = "ck_test_key_do_not_leak";

    record Req(String method, String uri, Headers headers, byte[] body) {
        String header(String name) {
            return headers.getFirst(name);
        }

        String bodyText() {
            return new String(body, StandardCharsets.UTF_8);
        }
    }

    record Reply(int status, Map<String, String> headers, byte[] body) {
        static Reply json(int status, String body) {
            return new Reply(status, Map.of("Content-Type", "application/json"), body == null ? new byte[0] : body.getBytes(StandardCharsets.UTF_8));
        }

        static Reply status(int status) {
            return new Reply(status, Map.of(), new byte[0]);
        }

        Reply with(String k, String v) {
            Map<String, String> h = new java.util.HashMap<>(headers);
            h.put(k, v);
            return new Reply(status, h, body);
        }
    }

    final HttpServer server;
    final List<Req> requests = new CopyOnWriteArrayList<>();
    final List<Long> sleeps = new CopyOnWriteArrayList<>();
    volatile Function<Req, Reply> handler = r -> Reply.json(200, "{}");

    TestHub() throws IOException {
        server = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
        server.createContext("/", ex -> {
            byte[] body = ex.getRequestBody().readAllBytes();
            Req req = new Req(ex.getRequestMethod(), ex.getRequestURI().getRawPath()
                    + (ex.getRequestURI().getRawQuery() == null ? "" : "?" + ex.getRequestURI().getRawQuery()), ex.getRequestHeaders(), body);
            requests.add(req);
            Reply reply = handler.apply(req);
            reply.headers().forEach((k, v) -> ex.getResponseHeaders().add(k, v));
            boolean empty = reply.body().length == 0 || reply.status() == 204;
            ex.sendResponseHeaders(reply.status(), empty ? -1 : reply.body().length);
            if (!empty) {
                try (OutputStream os = ex.getResponseBody()) {
                    os.write(reply.body());
                }
            }
            ex.close();
        });
        server.start();
    }

    TestHub on(Function<Req, Reply> h) {
        this.handler = h;
        return this;
    }

    TestHub always(Reply r) {
        return on(req -> r);
    }

    /** Answers with the given replies in turn, then repeats the last. */
    TestHub script(Reply... replies) {
        return on(req -> replies[Math.min(requests.size() - 1, replies.length - 1)]);
    }

    String url() {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    Hansho client() {
        return client(b -> {});
    }

    /** A client against this hub whose retry waits are recorded in {@link #sleeps} instead of slept. */
    Hansho client(Consumer<Hansho.Builder> tweak) {
        Hansho.Builder b = Hansho.builder(url(), KEY).sleeper(sleeps::add);
        tweak.accept(b);
        return b.build();
    }

    List<String> uris() {
        List<String> out = new ArrayList<>();
        requests.forEach(r -> out.add(r.uri()));
        return out;
    }

    @Override
    public void close() {
        server.stop(0);
    }
}
