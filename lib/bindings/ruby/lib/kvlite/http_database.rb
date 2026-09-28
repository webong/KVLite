require "base64"
require "json"
require "net/http"
require "timeout"
require "uri"
require_relative "errors"
require_relative "native_database"

module KVLite
  # Remote JSON/HTTP adapter. The server, not this client, owns database paths.
  class HttpDatabase
    def initialize(base_url, token: nil, timeout_seconds: 30, driver: nil, requester: nil)
      @base_url = URI.parse(base_url.to_s)
      unless %w[http https].include?(@base_url.scheme) && @base_url.host && @base_url.userinfo.nil?
        raise InvalidArgumentError, "KVLite remote URL must be a valid http(s) URL without userinfo"
      end
      unless timeout_seconds.is_a?(Numeric) && timeout_seconds.positive?
        raise InvalidArgumentError, "KVLite HTTP timeout must be positive"
      end
      if requester && !requester.respond_to?(:call)
        raise InvalidArgumentError, "KVLite HTTP requester must be callable"
      end

      @base_url = base_url.to_s.sub(%r{/+\z}, "")
      @token = token
      @timeout_seconds = timeout_seconds
      @driver = NativeDatabase.normalize_driver(driver)
      @requester = requester
    rescue URI::InvalidURIError => error
      raise InvalidArgumentError, "KVLite remote URL is invalid: #{error.message}"
    end

    def put(key, value, ttl_seconds: 0)
      unless ttl_seconds.is_a?(Integer) && ttl_seconds.between?(0, NativeLibrary::MAX_TTL_SECONDS)
        raise InvalidArgumentError, "KVLite TTL must be a non-negative number of seconds"
      end
      path = "/v1/entries/#{encoded_key(key)}"
      path += "?ttl_seconds=#{ttl_seconds}" if ttl_seconds.positive?
      status, body = request("PUT", path, JSON.generate(value))
      raise response_error(status, body) unless status == 204

      nil
    rescue JSON::GeneratorError => error
      raise SerializationError, "KVLite JSON serialization failed: #{error.message}"
    end

    def get(key)
      status, body = request("GET", "/v1/entries/#{encoded_key(key)}")
      raise NotFoundError, "KVLite key was not found" if status == 404
      raise response_error(status, body) unless status == 200

      JSON.parse(body)
    rescue JSON::ParserError => error
      raise SerializationError, "KVLite JSON deserialization failed: #{error.message}"
    end

    def delete(key)
      status, body = request("DELETE", "/v1/entries/#{encoded_key(key)}")
      raise response_error(status, body) unless status == 204

      nil
    end

    def close
      nil
    end

    private

    def encoded_key(key)
      raise InvalidArgumentError, "KVLite key must be a non-empty String" unless key.is_a?(String) && !key.empty?

      Base64.strict_encode64(key.b).tr("+/", "-_").delete("=")
    end

    def request(method, path, body = nil)
      headers = { "Accept" => "application/json" }
      headers["Content-Type"] = "application/json" unless body.nil?
      headers["Authorization"] = "Bearer #{@token}" unless @token.nil? || @token.empty?
      headers["X-KVLite-Driver"] = @driver if @driver
      url = @base_url + path
      return @requester.call(method, url, body, headers) if @requester

      uri = URI.parse(url)
      request_class = { "PUT" => Net::HTTP::Put, "GET" => Net::HTTP::Get,
        "DELETE" => Net::HTTP::Delete }.fetch(method)
      http_request = request_class.new(uri)
      headers.each { |name, value| http_request[name] = value }
      http_request.body = body unless body.nil?
      response = Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https",
        open_timeout: @timeout_seconds, read_timeout: @timeout_seconds) do |http|
        http.request(http_request)
      end
      [response.code.to_i, response.body.to_s]
    rescue IOError, SocketError, Timeout::Error, SystemCallError => error
      raise StorageError, "KVLite HTTP request failed: #{error.message}"
    end

    def response_error(status, body)
      message = body.to_s.strip
      message = "KVLite HTTP request failed with status #{status}" if message.empty?
      StorageError.new(message)
    end
  end
end
