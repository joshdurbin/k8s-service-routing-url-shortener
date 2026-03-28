-- wrk script: write load test (POST /shorten). See TEACHING.md.
-- Usage: wrk -t4 -c50 -d30s -s scripts/create.lua http://localhost:8080

local counter = 0

function setup(thread)
    thread:set("id", counter)
    counter = counter + 1
end

function init(args)
    math.randomseed(os.time() + _G.id)
end

local domains = {"example.com", "test.org", "demo.net", "sample.io"}
local paths = {"articles", "posts", "users", "products", "pages", "docs", "api"}

local function random_string(n)
    local chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
    local t = {}
    for i = 1, n do
        local idx = math.random(#chars)
        t[i] = chars:sub(idx, idx)
    end
    return table.concat(t)
end

local function random_url()
    local domain = domains[math.random(#domains)]
    local parts = {}
    for i = 1, math.random(1, 3) do
        parts[i] = math.random() > 0.5 and paths[math.random(#paths)] or random_string(math.random(4, 10))
    end
    return "https://" .. domain .. "/" .. table.concat(parts, "/")
end

function request()
    local body = '{"long_url":"' .. random_url() .. '"}'
    return wrk.format("POST", "/shorten", {["Content-Type"] = "application/json"}, body)
end

function response(status, headers, body)
    if status >= 400 then
        io.write("error " .. status .. ": " .. body:sub(1, 120) .. "\n")
    end
end

function done(summary, latency, requests)
    io.write("\n--- Create Load Test ---\n")
    io.write(string.format("Requests:    %d\n", summary.requests))
    io.write(string.format("Errors:      %d\n", summary.errors.status))
    io.write(string.format("Timeouts:    %d\n", summary.errors.timeout))
    io.write(string.format("Duration:    %.2fs\n", summary.duration / 1e6))
    io.write(string.format("Req/sec:     %.2f\n", summary.requests / (summary.duration / 1e6)))
    io.write(string.format("Avg latency: %.2fms\n", latency.mean / 1000))
    io.write(string.format("P99 latency: %.2fms\n", latency:percentile(99) / 1000))
end
