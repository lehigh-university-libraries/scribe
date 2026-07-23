#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s [terraform-directory]\n' "${0##*/}" >&2
}

die() {
  printf 'normalize-terraform-moved-state: %s\n' "$*" >&2
  exit 1
}

if (( $# > 1 )); then
  usage
  exit 2
fi

terraform_dir=${1:-terraform}
terraform_bin=${TERRAFORM_BIN:-terraform}
lock_timeout=${TF_STATE_LOCK_TIMEOUT:-5m}

command -v "$terraform_bin" >/dev/null 2>&1 || die "Terraform is required (set TERRAFORM_BIN to override its executable)."
command -v jq >/dev/null 2>&1 || die "jq is required; install jq and retry."
command -v realpath >/dev/null 2>&1 || die "realpath is required; install GNU coreutils and retry."

terraform_dir=$(realpath -e -- "$terraform_dir") || die "Terraform directory does not exist: $terraform_dir"
[[ -d "$terraform_dir" ]] || die "Terraform path is not a directory: $terraform_dir"

modules_root="$terraform_dir/.terraform/modules"
modules_json="$modules_root/modules.json"
[[ -f "$modules_json" ]] || die "module manifest is missing; run terraform -chdir=$terraform_dir init first"
modules_root=$(realpath -e -- "$modules_root") || die "Terraform module cache is unavailable: $modules_root"

module_field() {
  local key=$1
  local field=$2

  jq -er --arg key "$key" --arg field "$field" '
    if (.Modules | type) != "array" then
      error("Modules must be an array")
    else
      [.Modules[] | select(.Key == $key)] |
      if length != 1 then
        error("expected exactly one module entry for " + $key)
      elif (.[0][$field] | type) != "string" or (.[0][$field] | length) == 0 then
        error("module " + $key + " has no valid " + $field)
      else
        .[0][$field]
      end
    end
  ' "$modules_json"
}

resolve_module_dir() {
  local key=$1
  local relative_dir resolved_dir

  relative_dir=$(module_field "$key" Dir) || die "invalid module manifest entry for $key"
  if [[ "$relative_dir" = /* ]]; then
    resolved_dir=$(realpath -e -- "$relative_dir") || die "module directory for $key does not exist"
  else
    resolved_dir=$(realpath -e -- "$terraform_dir/$relative_dir") || die "module directory for $key does not exist"
  fi

  case "$resolved_dir/" in
    "$modules_root/"*) ;;
    *) die "module directory for $key escapes the Terraform module cache: $resolved_dir" ;;
  esac
  printf '%s\n' "$resolved_dir"
}

scribe_source=$(module_field scribe Source) || die "invalid module manifest source for scribe"
case "$scribe_source" in
  *github.com/libops/cloud-compose*) ;;
  *) die "module scribe is not the expected libops/cloud-compose upstream module" ;;
esac

scribe_dir=$(resolve_module_dir scribe)
gcp_dir=$(resolve_module_dir scribe.gcp)
case "$gcp_dir/" in
  "$scribe_dir/"*) ;;
  *) die "module scribe.gcp is not nested beneath the installed scribe module" ;;
esac

root_moved="$scribe_dir/moved.tf"
repo_moved="$terraform_dir/cloud_compose_moved.tf"
repo_root_moved="$terraform_dir/scribe_root_moved.tf"
gcp_moved="$gcp_dir/moved.tf"
for moved_file in "$root_moved" "$repo_moved" "$repo_root_moved" "$gcp_moved"; do
  [[ -f "$moved_file" ]] || die "required moved declaration file is missing: $moved_file"
done

parse_moved_file() {
  local phase=$1
  local prefix=$2
  local moved_file=$3

  awk -v phase="$phase" -v prefix="$prefix" '
    function fail(message) {
      printf "%s:%d: %s\n", FILENAME, FNR, message > "/dev/stderr"
      failed = 1
      exit 1
    }
    function value(line, key, result) {
      result = line
      sub("^[[:space:]]*" key "[[:space:]]*=[[:space:]]*", "", result)
      sub("[[:space:]]*(#|//).*$", "", result)
      sub("[[:space:]]+$", "", result)
      return result
    }
    function valid_address(address) {
      return address ~ /^[[:alnum:]_.-]+(\[[[:alnum:]_".-]+\])?(\.[[:alnum:]_-]+(\[[[:alnum:]_".-]+\])?)*$/
    }
    /^[[:space:]]*($|#|\/\/)/ { next }
    !inside && /^[[:space:]]*moved[[:space:]]*\{[[:space:]]*$/ {
      inside = 1
      from = ""
      to = ""
      next
    }
    inside && /^[[:space:]]*from[[:space:]]*=/ {
      if (from != "") fail("duplicate from assignment in moved block")
      from = value($0, "from")
      if (!valid_address(from)) fail("unsupported from address in moved block")
      next
    }
    inside && /^[[:space:]]*to[[:space:]]*=/ {
      if (to != "") fail("duplicate to assignment in moved block")
      to = value($0, "to")
      if (!valid_address(to)) fail("unsupported to address in moved block")
      next
    }
    inside && /^[[:space:]]*\}[[:space:]]*$/ {
      if (from == "" || to == "") fail("moved block must contain exactly one from and one to")
      if (prefix != "") {
        from = prefix "." from
        to = prefix "." to
      }
      printf "%s\t%s\t%s\n", phase, from, to
      count++
      inside = 0
      next
    }
    { fail("unsupported content; only simple moved blocks are accepted") }
    END {
      if (!failed && inside) fail("unterminated moved block")
      if (!failed && count == 0) fail("no moved blocks found")
    }
  ' "$moved_file"
}

declare -a state_addresses=()
declare -a temporary_files=()

cleanup_temporary_files() {
  local path
  for path in "${temporary_files[@]}"; do
    rm -f -- "$path"
  done
}
trap cleanup_temporary_files EXIT

load_state() {
  local state_output
  if ! state_output=$("$terraform_bin" "-chdir=$terraform_dir" state list); then
    die "unable to list Terraform state; select an initialized workspace with existing state"
  fi
  state_addresses=()
  if [[ -n "$state_output" ]]; then
    mapfile -t state_addresses <<<"$state_output"
  fi
}

is_module_address() {
  local address=$1
  local module_pattern='^module\.[[:alnum:]_-]+(\[[^][]+\])?(\.module\.[[:alnum:]_-]+(\[[^][]+\])?)*$'
  [[ "$address" =~ $module_pattern ]]
}

address_matches() {
  local endpoint=$1
  local candidate=$2

  [[ "$candidate" == "$endpoint" ]] && return 0
  if is_module_address "$endpoint"; then
    [[ "$candidate" == "$endpoint".* ]] && return 0
    [[ "$endpoint" != *']' && "$candidate" == "$endpoint"\[* ]] && return 0
  elif [[ "$endpoint" != *']' && "$candidate" == "$endpoint"\[* ]]; then
    return 0
  fi
  return 1
}

source_count=0
destination_count=0
source_sample=''
destination_sample=''

classify_pair() {
  local source=$1
  local destination=$2
  local candidate source_match destination_match

  source_count=0
  destination_count=0
  source_sample=''
  destination_sample=''

  for candidate in "${state_addresses[@]}"; do
    source_match=false
    destination_match=false
    address_matches "$source" "$candidate" && source_match=true
    address_matches "$destination" "$candidate" && destination_match=true

    if [[ "$source_match" == true && "$destination_match" == true ]]; then
      if (( ${#source} > ${#destination} )); then
        destination_match=false
      elif (( ${#destination} > ${#source} )); then
        source_match=false
      else
        die "ambiguous state address $candidate matches both $source and $destination"
      fi
    fi

    if [[ "$source_match" == true ]]; then
      (( source_count += 1 ))
      [[ -n "$source_sample" ]] || source_sample=$candidate
    fi
    if [[ "$destination_match" == true ]]; then
      (( destination_count += 1 ))
      [[ -n "$destination_sample" ]] || destination_sample=$candidate
    fi
  done
}

process_pair() {
  local phase=$1
  local source=$2
  local destination=$3

  classify_pair "$source" "$destination"
  if (( source_count > 0 && destination_count > 0 )); then
    die "$phase move conflicts: source $source ($source_sample) and destination $destination ($destination_sample) both exist"
  elif (( source_count > 0 )); then
    printf 'Moving %s state: %s -> %s (%d object(s))\n' "$phase" "$source" "$destination" "$source_count"
    "$terraform_bin" "-chdir=$terraform_dir" state mv -lock-timeout="$lock_timeout" "$source" "$destination"
    load_state
    classify_pair "$source" "$destination"
    if (( source_count > 0 || destination_count == 0 )); then
      die "$phase move did not reach its requested destination: $source -> $destination"
    fi
  elif (( destination_count > 0 )); then
    printf 'Already normalized %s state: %s -> %s\n' "$phase" "$source" "$destination"
  else
    printf 'Absent %s state: %s -> %s\n' "$phase" "$source" "$destination"
  fi
}

process_moved_file() {
  local phase=$1
  local prefix=$2
  local moved_file=$3
  local parsed_file record_phase source destination

  parsed_file=$(mktemp)
  temporary_files+=("$parsed_file")
  if ! parse_moved_file "$phase" "$prefix" "$moved_file" >"$parsed_file"; then
    rm -f -- "$parsed_file"
    die "could not safely parse moved declarations in $moved_file"
  fi

  while IFS=$'\t' read -r record_phase source destination; do
    [[ "$record_phase" == "$phase" && -n "$source" && -n "$destination" ]] || {
      die "invalid parsed moved declaration in $moved_file"
    }
    process_pair "$phase" "$source" "$destination"
  done <"$parsed_file"
  rm -f -- "$parsed_file"
}

preflight_moved_file() {
  local phase=$1
  local prefix=$2
  local moved_file=$3
  local parsed_file record_phase source destination

  parsed_file=$(mktemp)
  temporary_files+=("$parsed_file")
  if ! parse_moved_file "$phase" "$prefix" "$moved_file" >"$parsed_file"; then
    rm -f -- "$parsed_file"
    die "could not safely parse moved declarations in $moved_file"
  fi

  while IFS=$'\t' read -r record_phase source destination; do
    [[ "$record_phase" == "$phase" && -n "$source" && -n "$destination" ]] || {
      die "invalid parsed moved declaration in $moved_file"
    }
    classify_pair "$source" "$destination"
    if (( source_count > 0 && destination_count > 0 )); then
      die "$phase move conflicts: source $source ($source_sample) and destination $destination ($destination_sample) both exist"
    fi
  done <"$parsed_file"
  rm -f -- "$parsed_file"
}

process_repository_hop() {
  local uncounted_module='module.scribe.module.gcp'
  local counted_module='module.scribe.module.gcp[0]'
  local uncounted_prefix="$uncounted_module."
  local counted_prefix="$counted_module."
  local parsed_file record_phase source destination inner_source inner_destination

  # Moving the whole module is required because the upstream root moved file
  # contains resources that are intentionally absent from the repository's
  # declarations. Per-resource moves would strand those objects in the
  # uncounted intermediate module address.
  process_pair repository-module "$uncounted_module" "$counted_module"

  parsed_file=$(mktemp)
  temporary_files+=("$parsed_file")
  if ! parse_moved_file repository-inner '' "$repo_moved" >"$parsed_file"; then
    rm -f -- "$parsed_file"
    die "could not safely parse moved declarations in $repo_moved"
  fi

  while IFS=$'\t' read -r record_phase source destination; do
    [[ "$record_phase" == repository-inner && "$source" == "$uncounted_prefix"* && "$destination" == "$counted_prefix"* ]] || {
      die "repository moved declaration does not describe the expected counted GCP module hop: $source -> $destination"
    }
    inner_source=${source#"$uncounted_prefix"}
    inner_destination=${destination#"$counted_prefix"}
    [[ -n "$inner_source" && -n "$inner_destination" ]] || {
      die "repository moved declaration has an empty inner address: $source -> $destination"
    }

    # Declarations whose inner addresses are equal are already covered by the
    # whole-module move. Only count/for_each changes inside the counted module
    # require another state operation.
    if [[ "$inner_source" != "$inner_destination" ]]; then
      process_pair repository-inner "$counted_prefix$inner_source" "$counted_prefix$inner_destination"
    fi
  done <"$parsed_file"
  rm -f -- "$parsed_file"
}

load_state
preflight_moved_file repository-root '' "$repo_root_moved"
process_moved_file repository-root '' "$repo_root_moved"
process_moved_file upstream-root module.scribe "$root_moved"
process_repository_hop
process_moved_file upstream-gcp 'module.scribe.module.gcp[0]' "$gcp_moved"

printf 'Terraform moved-state normalization completed without provider refresh or apply.\n'
