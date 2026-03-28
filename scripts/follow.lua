-- wrk script: follow load test (GET /{code}). See TEACHING.md.
-- Fetches all codes from admin API before workers start.
-- Usage: wrk -t4 -c50 -d30s -s scripts/follow.lua http://localhost:8080

local admin_host = os.getenv("ADMIN_HOST") or "localhost"
local admin_port = os.getenv("ADMIN_PORT") or "8082"
local codes_csv = nil
local codes_count = 0
local counter = 0

local function fetch_page(token)
    local url = string.format("http://%s:%s/admin/v0/urls", admin_host, admin_port)
    if token ~= "" then url = url .. "?page_token=" .. token end
    local f = io.popen("curl -sf '" .. url .. "'")
    local body = f:read("*a")
    f:close()
    return body
end

local function fetch_all_codes()
    local all, token = {}, ""
    repeat
        local body = fetch_page(token)
        for code in body:gmatch('"short_code"%s*:%s*"([A-Za-z0-9]+)"') do
            table.insert(all, code)
        end
        token = body:match('"next_page_token"%s*:%s*"([^"]*)"') or ""
    until token == ""
    return all
end

function setup(thread)
    if codes_csv == nil then
        io.write(string.format("Fetching codes from http://%s:%s/admin/v0/urls ...\n", admin_host, admin_port))
        local codes = fetch_all_codes()
        codes_count = #codes
        if codes_count == 0 then
            io.write("ERROR: no short codes found — run 'make loadtest-create' first\n")
            os.exit(1)
        end
        io.write(string.format("Loaded %d short codes.\n", codes_count))
        codes_csv = table.concat(codes, ",")
    end
    thread:set("id", counter)
    thread:set("codes_csv", codes_csv)
    counter = counter + 1
end

local thread_codes = {}

function init(args)
    math.randomseed(os.time() + _G.id)
    thread_codes = {}
    for code in _G.codes_csv:gmatch("[^,]+") do
        table.insert(thread_codes, code)
    end
end

function request()
    local code = thread_codes[math.random(#thread_codes)]
    return wrk.format("GET", "/" .. code)
end

function response(status, headers, body)
    if status ~= 302 and status >= 400 then
        io.write("error " .. status .. ": " .. body:sub(1, 120) .. "\n")
    end
end

function done(summary, latency, requests)
    io.write("\n--- Follow Load Test ---\n")
    io.write(string.format("Short codes: %d\n", codes_count))
    io.write(string.format("Requests:    %d\n", summary.requests))
    io.write(string.format("Errors:      %d\n", summary.errors.status))
    io.write(string.format("Timeouts:    %d\n", summary.errors.timeout))
    io.write(string.format("Duration:    %.2fs\n", summary.duration / 1e6))
    io.write(string.format("Req/sec:     %.2f\n", summary.requests / (summary.duration / 1e6)))
    io.write(string.format("Avg latency: %.2fms\n", latency.mean / 1000))
    io.write(string.format("P99 latency: %.2fms\n", latency:percentile(99) / 1000))
end
