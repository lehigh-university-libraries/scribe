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
repo_application_network_moved="$terraform_dir/application_network_moved.tf"
gcp_moved="$gcp_dir/moved.tf"
for moved_file in "$root_moved" "$repo_moved" "$repo_root_moved" "$gcp_moved" "$repo_application_network_moved"; do
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

declare -a application_network_lineage_aliases=()

append_application_network_lineage_alias() {
  local candidate=$1
  local existing

  [[ -n "$candidate" ]] || die "application-network lineage contains an empty address"
  for existing in "${application_network_lineage_aliases[@]}"; do
    [[ "$existing" == "$candidate" ]] && return 0
  done
  application_network_lineage_aliases+=("$candidate")
}

# The application network and subnet finish outside module.scribe, but an old
# workspace can still hold either object at the original module root, the
# intermediate uncounted GCP module, or the current counted GCP module. Detect
# every conflicting source/destination combination before the first state mv;
# otherwise discovering a root destination only in the final phase would leave
# the earlier lineage hops partially committed.
preflight_application_network_moves() {
  local uncounted_module='module.scribe.module.gcp'
  local counted_module='module.scribe.module.gcp[0]'
  local uncounted_prefix="$uncounted_module."
  local counted_prefix="$counted_module."
  local upstream_records repository_records gcp_records application_records
  local record_phase move_source move_destination
  local final_source final_destination alias alias_index
  local repository_inner_source repository_execution_source
  local candidate candidate_is_source
  local lineage_source_count lineage_destination_count
  local lineage_source_sample lineage_second_source_sample lineage_destination_sample

  upstream_records=$(mktemp)
  repository_records=$(mktemp)
  gcp_records=$(mktemp)
  application_records=$(mktemp)
  temporary_files+=("$upstream_records" "$repository_records" "$gcp_records" "$application_records")

  parse_moved_file upstream-root module.scribe "$root_moved" >"$upstream_records" ||
    die "could not safely parse moved declarations in $root_moved"
  parse_moved_file repository-inner '' "$repo_moved" >"$repository_records" ||
    die "could not safely parse moved declarations in $repo_moved"
  parse_moved_file upstream-gcp "$counted_module" "$gcp_moved" >"$gcp_records" ||
    die "could not safely parse moved declarations in $gcp_moved"
  parse_moved_file application-network '' "$repo_application_network_moved" >"$application_records" ||
    die "could not safely parse moved declarations in $repo_application_network_moved"

  while IFS=$'\t' read -r record_phase final_source final_destination; do
    [[ "$record_phase" == application-network \
      && "$final_source" == "$counted_prefix"* \
      && "$final_destination" != module.* ]] || {
      die "application-network moved declaration must move one counted GCP-module object to the root: $final_source -> $final_destination"
    }

    application_network_lineage_aliases=()
    append_application_network_lineage_alias "$final_source"
    alias_index=0
    while (( alias_index < ${#application_network_lineage_aliases[@]} )); do
      alias=${application_network_lineage_aliases[$alias_index]}

      # process_repository_hop first moves the whole uncounted module. Include
      # the same inner address before that hop even when no repository-specific
      # count/for_each declaration exists for this resource.
      if [[ "$alias" == "$counted_prefix"* ]]; then
        append_application_network_lineage_alias "$uncounted_prefix${alias#"$counted_prefix"}"
      fi

      while IFS=$'\t' read -r record_phase move_source move_destination; do
        [[ "$record_phase" == upstream-root ]] ||
          die "invalid parsed upstream-root declaration in $root_moved"
        if address_matches "$move_destination" "$alias"; then
          append_application_network_lineage_alias "$move_source"
        fi
      done <"$upstream_records"

      while IFS=$'\t' read -r record_phase move_source move_destination; do
        [[ "$record_phase" == repository-inner \
          && "$move_source" == "$uncounted_prefix"* \
          && "$move_destination" == "$counted_prefix"* ]] || {
          die "repository moved declaration does not describe the expected counted GCP module hop: $move_source -> $move_destination"
        }
        if address_matches "$move_destination" "$alias"; then
          repository_inner_source=${move_source#"$uncounted_prefix"}
          repository_execution_source="$counted_prefix$repository_inner_source"
          append_application_network_lineage_alias "$repository_execution_source"
          append_application_network_lineage_alias "$move_source"
        fi
      done <"$repository_records"

      while IFS=$'\t' read -r record_phase move_source move_destination; do
        [[ "$record_phase" == upstream-gcp ]] ||
          die "invalid parsed upstream-gcp declaration in $gcp_moved"
        if address_matches "$move_destination" "$alias"; then
          append_application_network_lineage_alias "$move_source"
        fi
      done <"$gcp_records"

      (( alias_index += 1 ))
    done

    lineage_source_count=0
    lineage_destination_count=0
    lineage_source_sample=''
    lineage_second_source_sample=''
    lineage_destination_sample=''
    for candidate in "${state_addresses[@]}"; do
      candidate_is_source=false
      for alias in "${application_network_lineage_aliases[@]}"; do
        if address_matches "$alias" "$candidate"; then
          candidate_is_source=true
          break
        fi
      done
      if [[ "$candidate_is_source" == true ]]; then
        (( lineage_source_count += 1 ))
        if [[ -z "$lineage_source_sample" ]]; then
          lineage_source_sample=$candidate
        elif [[ -z "$lineage_second_source_sample" ]]; then
          lineage_second_source_sample=$candidate
        fi
      fi
      if address_matches "$final_destination" "$candidate"; then
        (( lineage_destination_count += 1 ))
        [[ -n "$lineage_destination_sample" ]] || lineage_destination_sample=$candidate
      fi
    done

    if (( lineage_source_count > 0 && lineage_destination_count > 0 )); then
      die "application-network preflight conflict: source lineage $lineage_source_sample and root destination $lineage_destination_sample both exist"
    fi
    if (( lineage_source_count > 1 )); then
      die "application-network preflight conflict: source aliases $lineage_source_sample and $lineage_second_source_sample both exist for root destination $final_destination"
    fi
    if (( lineage_destination_count > 1 )); then
      die "application-network preflight conflict: root destination $final_destination has multiple state objects"
    fi
  done <"$application_records"

  rm -f -- "$upstream_records" "$repository_records" "$gcp_records" "$application_records"
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
preflight_application_network_moves
process_moved_file repository-root '' "$repo_root_moved"
process_moved_file upstream-root module.scribe "$root_moved"
process_repository_hop
process_moved_file upstream-gcp 'module.scribe.module.gcp[0]' "$gcp_moved"
process_moved_file application-network '' "$repo_application_network_moved"

printf 'Terraform moved-state normalization completed without provider refresh or apply.\n'
