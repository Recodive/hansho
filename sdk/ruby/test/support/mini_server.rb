# frozen_string_literal: true

require "socket"

# A tiny HTTP/1.1 server on a thread (standard library only), used as the fake hub in
# the unit tests and as the webhook receiver in the integration test. It records every
# request exactly as received. The block gets the Request and returns
# [status, headers, body]; the default answer is 200 {}.
class MiniServer
  Request = Struct.new(:method, :target, :headers, :body, keyword_init: true)

  attr_reader :requests, :errors

  def initialize(host: "127.0.0.1", port: 0, &handler)
    @handler = handler
    @requests = []
    @errors = []
    @mutex = Mutex.new
    @server = TCPServer.new(host, port)
    @host = host
    @threads = []
    @thread = Thread.new { accept_loop }
  end

  def port
    @server.addr[1]
  end

  def url
    "http://#{@host}:#{port}"
  end

  def stop
    @server.close unless @server.closed?
    @thread.kill
    @threads.each(&:kill)
  end

  private

  def accept_loop
    loop do
      sock = @server.accept
      @threads << Thread.new(sock) { |s| serve(s) }
    end
  rescue IOError, Errno::EBADF
    nil
  end

  def serve(sock)
    req = read_request(sock)
    return unless req

    @mutex.synchronize { @requests << req }
    status, headers, body = begin
      @handler ? @handler.call(req) : [200, {}, "{}"]
    rescue Exception => e # rubocop:disable Lint/RescueException
      @mutex.synchronize { @errors << e }
      [500, {}, ""]
    end
    status ||= 200
    headers ||= {}
    body = body.to_s.b
    out = +"HTTP/1.1 #{status} X\r\n"
    headers = { "Content-Type" => "application/json" }.merge(headers)
    headers.each { |k, v| out << "#{k}: #{v}\r\n" }
    out << "Content-Length: #{body.bytesize}\r\n" unless status == 204
    out << "Connection: close\r\n\r\n"
    sock.write(out.b)
    sock.write(body) unless status == 204
  rescue Errno::EPIPE, Errno::ECONNRESET, IOError
    nil
  ensure
    sock.close unless sock.closed?
  end

  def read_request(sock)
    line = sock.gets("\r\n")
    return nil unless line

    method, target, = line.split(" ", 3)
    headers = {}
    while (l = sock.gets("\r\n")) && l != "\r\n"
      k, v = l.chomp.split(":", 2)
      headers[k.downcase] = v.to_s.strip
    end
    body = +""
    if headers["transfer-encoding"].to_s.downcase.include?("chunked")
      loop do
        size = sock.gets("\r\n").to_i(16)
        if size.zero?
          sock.gets("\r\n")
          break
        end
        body << sock.read(size)
        sock.read(2)
      end
    elsif (len = headers["content-length"].to_i).positive?
      body << sock.read(len)
    end
    Request.new(method: method, target: target, headers: headers, body: body.b)
  end
end
