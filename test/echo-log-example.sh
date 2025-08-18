#!/bin/bash

# This script tests the console forwarding functionality and can trigger issues with very long lines.
# The bug was originally discovered in the SSH server.

printf "[main/INFO] [net.minecraft.world.item.crafting.RecipeManager/]: Loaded 5692 recipes\r\n"
printf "[START] If this is the last line you see, you've hit a bug related to line limits...\r\n"

declare -i count=64000 l_min=4 l_max=8
declare -a rand_strings=()

declare -i i=0
while [ $i -lt $count ]; do
  declare -i l=$((RANDOM%(l_max-l_min+1)+l_min))
  read -r -N $l rand_strings[i]
  i=$((i+1))
done < <(
  LC_ALL=POSIX
  tr -dc '[:alnum:]' </dev/urandom
)

printf '%q' "${rand_strings[@]}"
printf "\r\n"

printf "[DONE] If you see this, things are okay.\r\n"
