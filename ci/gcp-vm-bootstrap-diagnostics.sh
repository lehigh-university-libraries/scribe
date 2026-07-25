#!/usr/bin/env bash

set -euo pipefail

readonly QUERY_TIMEOUT_SECONDS=45
readonly BACKEND_PORT=80

fail() {
  printf 'GCP VM diagnostics failed: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

render_instance() {
  local expected_name="$1" expected_zone="$2"
  jq -er --arg expected_name "$expected_name" --arg expected_zone "$expected_zone" '
    (.id // "" | tostring) as $id |
    (.name // "" | tostring) as $name |
    (.status // "" | tostring) as $status |
    (.creationTimestamp // "unknown" | tostring) as $created |
    (.lastStartTimestamp // "unknown" | tostring) as $started |
    (.lastStopTimestamp // "unknown" | tostring) as $stopped |
    (.zone // "" | tostring | split("/") | last) as $zone |
    ([.disks[]?] | length) as $disk_count |
    ([.disks[]? | select(.boot == true) | (.deviceName // "unknown" | tostring)] |
      if length == 0 then "unknown" else .[0] end) as $boot_disk |
    select($id | test("^[0-9]{1,30}$")) |
    select($name == $expected_name) |
    select($status | test("^(PROVISIONING|STAGING|RUNNING|STOPPING|SUSPENDING|SUSPENDED|REPAIRING|TERMINATED)$")) |
    select($created | test("^(unknown|[0-9TZ:+.-]{10,40})$")) |
    select($started | test("^(unknown|[0-9TZ:+.-]{10,40})$")) |
    select($stopped | test("^(unknown|[0-9TZ:+.-]{10,40})$")) |
    select($zone == $expected_zone) |
    select($disk_count >= 0 and $disk_count <= 16) |
    select($boot_disk | test("^(unknown|[a-z0-9][a-z0-9-]{0,62})$")) |
    [$id, $name, $status, $created, $started, $stopped, $zone, ($disk_count | tostring), $boot_disk] |
    @tsv
  '
}

render_instance_network() {
  local expected_network="$1" expected_subnetwork="$2"
  jq -er \
    --arg expected_network "$expected_network" \
    --arg expected_subnetwork "$expected_subnetwork" '
    def network_resource:
      strings
      | capture("(?<resource>projects/[^/]+/global/networks/[a-z][a-z0-9-]{0,62})$").resource;
    def subnetwork_resource:
      strings
      | capture("(?<resource>projects/[^/]+/regions/[a-z]+-[a-z]+[0-9]+/subnetworks/[a-z][a-z0-9-]{0,62})$").resource;

    (.networkInterfaces // []) as $interfaces
    | select(($interfaces | type) == "array" and ($interfaces | length) == 1)
    | ($interfaces[0].network | network_resource) as $network
    | ($interfaces[0].subnetwork | subnetwork_resource) as $subnetwork
    | ($interfaces[0].name // "") as $interface
    | select($interface == "nic0")
    | select($network == $expected_network and $subnetwork == $expected_subnetwork)
    | [$network, $subnetwork, $interface]
    | @tsv
  '
}

render_backend_network() {
  local expected_job="$1" expected_region="$2" expected_network="$3" expected_subnetwork="$4"
  jq -er \
    --arg expected_job "$expected_job" \
    --arg expected_region "$expected_region" \
    --arg expected_network "$expected_network" \
    --arg expected_subnetwork "$expected_subnetwork" '
      def leaf:
        if type == "string" then split("/") | last else "" end;
      def network_resource:
        strings
        | try capture("(?<resource>projects/[^/]+/global/networks/[a-z][a-z0-9-]{0,62})$").resource
          catch "";
      def subnetwork_resource:
        strings
        | try capture("(?<resource>projects/[^/]+/regions/[a-z]+-[a-z]+[0-9]+/subnetworks/[a-z][a-z0-9-]{0,62})$").resource
          catch "";

      ((.metadata.name // .name // "") | leaf) as $name
      | ((.metadata.labels["cloud.googleapis.com/location"] // "") | tostring) as $region
      | (.spec.template.metadata.annotations // {}) as $annotations
      | select(
          ($annotations | type) == "object"
          and (($annotations["run.googleapis.com/network-interfaces"] // null) | type) == "string"
          and (($annotations["run.googleapis.com/vpc-access-connector"] // "") == "")
        )
      | ($annotations["run.googleapis.com/network-interfaces"] | select(length <= 4096) | fromjson) as $interfaces
      | {
          egress: ($annotations["run.googleapis.com/vpc-access-egress"] // ""),
          networkInterfaces: $interfaces
        } as $vpc
      | ($vpc.networkInterfaces // []) as $interfaces
      | select(($interfaces | type) == "array" and ($interfaces | length) <= 8)
      | select($region == $expected_region)
      | (($vpc.egress // "") | tostring | ascii_upcase | gsub("-"; "_")) as $egress
      | [
          (if $name == $expected_job then "match" else "mismatch" end),
          (if $egress == "PRIVATE_RANGES_ONLY" then "private-ranges-only" else "mismatch" end),
          ($interfaces | length | tostring),
          (if ($interfaces | length) == 1
                and (($interfaces[0].network | network_resource) == $expected_network)
            then "match" else "mismatch" end),
          (if ($interfaces | length) == 1
                and (($interfaces[0].subnetwork | subnetwork_resource) == $expected_subnetwork)
            then "match" else "mismatch" end)
        ]
      | @tsv
    '
}

render_user_managed_key_summary() {
  local expected_project="$1" expected_account="$2"
  jq -er --arg expected_project "$expected_project" --arg expected_account "$expected_account" '
    def valid_key_name:
      type == "string" and (
        split("/") as $parts
        | ($parts | length) == 6
          and $parts[0] == "projects"
          and $parts[1] == $expected_project
          and $parts[2] == "serviceAccounts"
          and $parts[3] == $expected_account
          and $parts[4] == "keys"
          and ($parts[5] | test("^[A-Za-z0-9_-]+$"))
      );

    select(type == "array" and length <= 64)
    | [.[] | select(.keyType == "USER_MANAGED")] as $raw_keys
    | select(all($raw_keys[];
        (.name | valid_key_name) and
        ((.disabled // false) | type == "boolean")
      ))
    | [$raw_keys[] | {disabled: (.disabled // false)}] as $keys
    | select(($keys | length) <= 10)
    | [
        ($keys | length),
        ([$keys[] | select(.disabled == false)] | length),
        ([$keys[] | select(.disabled == true)] | length)
      ]
    | map(tostring)
    | @tsv
  '
}

render_effective_firewall() {
  local expected_rule="$1" expected_network="$2" expected_source_cidr="$3" expected_target_tag="$4"
  jq -er \
    --arg expected_rule "$expected_rule" \
    --arg expected_network "$expected_network" \
    --arg expected_source_cidr "$expected_source_cidr" \
    --arg expected_target_tag "$expected_target_tag" \
    --argjson expected_port "$BACKEND_PORT" '
      def normalized_rows:
        select(type == "object")
        | (.firewalls // []) as $vpc_rules
        | (.firewallPolicys // []) as $policies
        | select(
            ($vpc_rules | type) == "array"
            and ($policies | type) == "array"
            and ($vpc_rules | length) <= 512
            and ($policies | length) <= 64
          )
        | ($vpc_rules | map(. + {"_scribe_rule_family": "vpc"}))
          + [
              $policies[]? as $policy
              | ($policy.rules // [])[]?
              | . + {"_scribe_rule_family": "policy"}
            ];
      def lower:
        tostring | ascii_downcase;
      def enabled:
        (.disabled // false) == false;
      def ingress:
        ((.direction // "") | tostring | ascii_upcase) == "INGRESS";
      def ranges:
        (.sourceRanges // .match.srcIpRanges // []);
      def target_tags:
        (.targetTags // []);
      def network_resource:
        (.network // "")
        | strings
        | try capture("(?<resource>projects/[^/]+/global/networks/[a-z][a-z0-9-]{0,62})$").resource
          catch "";
      def port_includes($port):
        . as $ports
        | if ($ports | type) != "array" then false
          elif ($ports | length) == 0 then true
          else any($ports[]?;
            tostring as $entry
            | if ($entry | test("^[0-9]{1,5}$")) then
                (($entry | tonumber) == $port)
              elif ($entry | test("^[0-9]{1,5}-[0-9]{1,5}$")) then
                ($entry | split("-") | map(tonumber)) as $bounds
                | $bounds[0] <= $port and $port <= $bounds[1]
              else false
              end)
          end;
      def layer_is_tcp_port($port):
        ((.IPProtocol // .ipProtocol // .protocol // "") | lower) as $protocol
        | ($protocol == "tcp" or $protocol == "6" or $protocol == "all")
          and ((.ports // []) | port_includes($port));
      def allow_layers:
        if ((.allowed // null) | type) == "array" then .allowed
        elif ((.match.layer4Configs // null) | type) == "array"
            and ((.action // "") | lower) == "allow" then .match.layer4Configs
        else []
        end;
      def deny_layers:
        if ((.denied // null) | type) == "array" then .denied
        elif ((.match.layer4Configs // null) | type) == "array"
            and ((.action // "") | lower) == "deny" then .match.layer4Configs
        elif ((.action // "") | lower) == "deny" then
          [{"ipProtocol": "all", "ports": []}]
        else []
        end;
      def exact_tcp_allow($port):
        (allow_layers) as $layers
        | ($layers | length) == 1
          and (($layers[0].IPProtocol // $layers[0].ipProtocol // $layers[0].protocol // "") | lower) == "tcp"
          and (($layers[0].ports // []) == [($port | tostring)]);
      def ipv4_cidr:
        capture("^(?<address>(?:[0-9]{1,3}\\.){3}[0-9]{1,3})/(?<prefix>[0-9]{1,2})$") as $parts
        | ($parts.address | split(".") | map(tonumber)) as $octets
        | ($parts.prefix | tonumber) as $prefix
        | select(
            ($octets | length) == 4
            and all($octets[]; . >= 0 and . <= 255)
            and $prefix >= 0 and $prefix <= 32
          )
        | {
            address: (((($octets[0] * 256) + $octets[1]) * 256 + $octets[2]) * 256 + $octets[3]),
            prefix: $prefix
          };
      def cidr_overlaps($left; $right):
        (try ($left | ipv4_cidr) catch null) as $left_cidr
        | (try ($right | ipv4_cidr) catch null) as $right_cidr
        | if $left_cidr != null and $right_cidr != null then
            ([$left_cidr.prefix, $right_cidr.prefix] | min) as $prefix
            | (
            ($left_cidr.address / pow(2; 32 - $prefix) | floor)
            == ($right_cidr.address / pow(2; 32 - $prefix) | floor)
            )
          else false
          end;
      def family:
        (._scribe_rule_family // "unknown");
      def name_leaf:
        ((.name // "") | tostring | split("/") | last);
      def exact_expected_allow:
        name_leaf == $expected_rule
        and enabled
        and ingress
        and network_resource == $expected_network
        and ((.priority // -1) | tonumber?) == 20
        and (ranges == [$expected_source_cidr])
        and (target_tags == [$expected_target_tag])
        and ((.sourceTags // []) == [])
        and ((.sourceServiceAccounts // []) == [])
        and ((.targetServiceAccounts // []) == [])
        and ((.destinationRanges // []) == [])
        and exact_tcp_allow($expected_port)
        and ((.denied // []) | length) == 0;
      def deny_applies_to_source:
        (ranges) as $ranges
        | ($ranges | type) == "array"
          and (
            ($ranges | length) == 0
            or any($ranges[]?; cidr_overlaps(.; $expected_source_cidr))
          );
      def tcp_port_deny:
        enabled
        and ingress
        and any(deny_layers[]?; layer_is_tcp_port($expected_port))
        and deny_applies_to_source;

      normalized_rows as $rows
      | select(($rows | length) <= 512)
      | ([$rows[] | select(name_leaf == $expected_rule)]) as $expected
      | ([$rows[] | select(exact_expected_allow)]) as $exact
      | ([
          $rows[]
          | select(
              tcp_port_deny
              and (
                family == "policy"
                or (((.priority // 65535) | tonumber?) <= 20)
              )
            )
        ]) as $denies
      | [
          (if ($exact | length) == 1 and ($expected | length) == 1 then "exact"
            elif ($expected | length) == 0 then "missing"
            else "drift"
            end),
          (if ($denies | length) == 0 then "absent" else "present" end),
          ($rows | length | tostring)
        ]
      | @tsv
    '
}

project="${GCLOUD_PROJECT:-}"
instance="${SCRIBE_INSTANCE:-}"
zone="${SCRIBE_ZONE:-}"
region="${SCRIBE_REGION:-}"
source_cidr="${TF_VAR_network_ip_cidr_range:-}"

[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || fail "GCLOUD_PROJECT must be a valid project ID"
if [[ "$instance" == scribe ]]; then
  workspace_slug=prod
elif [[ "$instance" =~ ^scribe-pr-([1-9][0-9]{0,9})$ ]]; then
  workspace_slug="pr-${BASH_REMATCH[1]}"
else
  fail "SCRIBE_INSTANCE must identify scribe or one scribe-pr-N preview instance"
fi
[[ "$zone" =~ ^[a-z]+-[a-z]+[0-9]+-[a-z]$ ]] || fail "SCRIBE_ZONE must be a valid GCP zone"
[[ "$region" =~ ^[a-z]+-[a-z]+[0-9]+$ && "$zone" == "$region"-[a-z] ]] ||
  fail "SCRIBE_REGION must match SCRIBE_ZONE"
[[ "$source_cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/(2[4-9]|3[0-2])$ ]] ||
  fail "TF_VAR_network_ip_cidr_range must be an IPv4 CIDR no broader than /24"

expected_network="projects/${project}/global/networks/${instance}"
expected_subnetwork="projects/${project}/regions/${region}/subnetworks/${instance}"

for command in gcloud jq mktemp timeout; do
  require_command "$command"
done

umask 077
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/scribe-gcp-vm-diagnostics.XXXXXX")"
trap 'rm -rf -- "$temp_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

instance_json="$temp_dir/instance.json"
backend_job_json="$temp_dir/backend-job.json"
effective_firewalls_json="$temp_dir/effective-firewalls.json"
app_keys_json="$temp_dir/app-keys.json"
internal_keys_json="$temp_dir/internal-keys.json"

printf 'Scribe GCP VM diagnostics (typed control-plane fields only)\n'
printf '== instance ==\n'
instance_record=""
instance_network_record=""
if timeout "$QUERY_TIMEOUT_SECONDS" gcloud compute instances describe "$instance" \
    --project "$project" \
    --zone "$zone" \
    --format=json >"$instance_json" 2>/dev/null &&
  instance_record="$(render_instance "$instance" "$zone" <"$instance_json" 2>/dev/null)"; then
  IFS=$'\t' read -r instance_id instance_name instance_status created started stopped instance_zone disk_count boot_disk <<<"$instance_record"
  printf '[instance] id=%s\n' "$instance_id"
  printf '[instance] name=%s\n' "$instance_name"
  printf '[instance] status=%s\n' "$instance_status"
  printf '[instance] creation_timestamp=%s\n' "$created"
  printf '[instance] last_start_timestamp=%s\n' "$started"
  printf '[instance] last_stop_timestamp=%s\n' "$stopped"
  printf '[instance] zone=%s\n' "$instance_zone"
  printf '[instance] attached_disk_count=%s\n' "$disk_count"
  printf '[instance] boot_disk=%s\n' "$boot_disk"
  printf '[status] instance_query=ok\n'
  instance_network_record="$(
    render_instance_network "$expected_network" "$expected_subnetwork" \
      <"$instance_json" 2>/dev/null || true
  )"
else
  describe_status=$?
  printf '[status] instance_query=unavailable exit=%s\n' "$describe_status"
fi

printf '== backend readiness VPC attachment ==\n'
backend_job="${instance}-${workspace_slug}-backend-readiness"
if [[ -z "$instance_network_record" ]]; then
  printf '[backend-network] query=unavailable identity=unknown egress=unknown interfaces=unknown network=unknown subnetwork=unknown\n'
elif timeout "$QUERY_TIMEOUT_SECONDS" gcloud run jobs describe "$backend_job" \
  --project "$project" \
  --region "$region" \
  --format=json >"$backend_job_json" 2>/dev/null; then
  backend_network_record="$(
    render_backend_network \
      "$backend_job" \
      "$region" \
      "$expected_network" \
      "$expected_subnetwork" \
      <"$backend_job_json" 2>/dev/null || true
  )"
  if [[ -n "$backend_network_record" ]]; then
    IFS=$'\t' read -r job_identity egress interface_count network_match subnetwork_match <<<"$backend_network_record"
    printf '[backend-network] query=ok identity=%s egress=%s interfaces=%s network=%s subnetwork=%s\n' \
      "$job_identity" "$egress" "$interface_count" "$network_match" "$subnetwork_match"
  else
    printf '[backend-network] query=invalid identity=unknown egress=unknown interfaces=unknown network=unknown subnetwork=unknown\n'
  fi
else
  printf '[backend-network] query=unavailable identity=unknown egress=unknown interfaces=unknown network=unknown subnetwork=unknown\n'
fi

printf '== managed service-account key capacity ==\n'
for identity in app internal; do
  case "$identity" in
    app)
      account="${instance}@${project}.iam.gserviceaccount.com"
      keys_json="$app_keys_json"
      ;;
    internal)
      account="internal-${instance}@${project}.iam.gserviceaccount.com"
      keys_json="$internal_keys_json"
      ;;
  esac
  if timeout "$QUERY_TIMEOUT_SECONDS" gcloud iam service-accounts keys list \
      --iam-account="$account" \
      --project "$project" \
      --format=json >"$keys_json" 2>/dev/null &&
    key_summary="$(render_user_managed_key_summary "$project" "$account" <"$keys_json" 2>/dev/null)"; then
    IFS=$'\t' read -r key_count enabled_key_count disabled_key_count <<<"$key_summary"
    if ((key_count == 10)); then
      capacity=exhausted
    else
      capacity=available
    fi
    printf '[service-account-keys] identity=%s query=ok user_managed=%s enabled=%s disabled=%s capacity=%s\n' \
      "$identity" "$key_count" "$enabled_key_count" "$disabled_key_count" "$capacity"
  else
    printf '[service-account-keys] identity=%s query=unavailable user_managed=unknown enabled=unknown disabled=unknown capacity=unknown\n' \
      "$identity"
  fi
done

printf '== VM effective firewall for backend ingress ==\n'
expected_firewall="allow-cloud-run-${instance}"
if timeout "$QUERY_TIMEOUT_SECONDS" gcloud compute instances network-interfaces get-effective-firewalls "$instance" \
  --project "$project" \
  --zone "$zone" \
  --network-interface=nic0 \
  --format=json >"$effective_firewalls_json" 2>/dev/null; then
  effective_firewall_record="$(
    render_effective_firewall \
      "$expected_firewall" \
      "$expected_network" \
      "$source_cidr" \
      "$instance" \
      <"$effective_firewalls_json" 2>/dev/null || true
  )"
  if [[ -n "$effective_firewall_record" ]]; then
    IFS=$'\t' read -r expected_allow matching_deny rule_count <<<"$effective_firewall_record"
    printf '[effective-firewall] query=ok expected_allow=%s matching_deny_candidate=%s rules=%s\n' \
      "$expected_allow" "$matching_deny" "$rule_count"
  else
    printf '[effective-firewall] query=invalid expected_allow=unknown matching_deny_candidate=unknown rules=unknown\n'
  fi
else
  printf '[effective-firewall] query=unavailable expected_allow=unknown matching_deny_candidate=unknown rules=unknown\n'
fi
