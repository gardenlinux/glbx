# Vision

Garden Linux is an image-centric, immutable operating system. Its deliverable is
not a distribution you install and then maintain — it is a finished system image,
a Kubernetes node, a VM host, a container base, built for one purpose and shipped
whole. Every package, every configuration, every version is decided at build time.
The image does not mutate in the field: no `apt-get upgrade` at runtime, no
configuration drift, no divergence between the system that was tested and the one
that runs. What you booted is what you built, down to the bit.

That model is only as trustworthy as the system that produces the images. **glbx**
is that system — the build executor beneath Garden Linux — and its ambition is to
make the guarantees the OS makes about itself true all the way down: that an image
is reproducible, that everything in it was built from source, and that every byte
can be traced back to where it came from. It is also a system that makes building
and maintaining Garden Linux dramatically less painful — turning what are today
slow, fragile, high-pressure workflows into fast, routine ones. This chapter is
about why all of that matters.

## An operating system you can account for, completely

Imagine being able to answer, for any image you ship, the question that most
organizations cannot: *where did this come from?* Not approximately, not "from
Debian" or "from our mirror," but exactly — every binary, every library, every
file traced back through the thing that built it to the version-controlled source
and the precise external inputs it was compiled from.

That is the center of the vision. Every single bit in the final image is accounted
for and can be traced back precisely to the source it originates from. There is no
opaque binary that someone, somewhere, built from something — no "trust the
maintainer, trust the mirror, trust the archive." The image is a closed,
auditable artifact: a complete bill of materials that is not a document bolted on
afterward but a property of how the image was made.

For anyone who has to answer to a regulator, a security team, or a customer's
compliance questionnaire, this changes the conversation. Provenance stops being a
best-effort reconstruction and becomes a fact about the build.

## Built from source — and only what you need

Everything that ships in a Garden Linux image is compiled from source under
control. Not one binary is pulled from a third-party mirror and dropped into the
output. That is a strong claim, and glbx is built so it holds by construction
rather than by policy: the moment an image is assembled, anything that is not
traceable to a controlled source is simply not in it.

Building from source unlocks something traditional distributions cannot easily do:
shipping *only what the image actually needs*. A conventional Linux image carries a
pile of machinery — `dpkg`, `apt`, a Perl interpreter, the whole "essential" base —
that exists only so the system can install and configure packages in the field. An
immutable image never installs anything in the field, so it never needs any of it.
glbx can produce images that leave that entire layer behind: smaller images, with
dramatically less attack surface, carrying nothing but the software they exist to
run. Less in the image means less to patch, less to audit, and less for an attacker
to reach.

## Reproducible, so trust is verifiable

Reproducibility is what turns provenance from a promise into something anyone can
check. Build the same image today, next month, or on a different continent, and you
get the same bytes. A result is a pure function of its declared inputs, so a build
is either a cache lookup of something already produced or a fresh computation that
lands on the identical answer.

This is the property that makes the whole system verifiable rather than merely
documented. If the inputs are known and the build is reproducible, then anyone —
a security auditor, a downstream consumer, a skeptical regulator — can rebuild the
image independently and confirm, bit for bit, that it is what it claims to be. No
privileged access to the build infrastructure, no "trust our logs." The build
itself is the evidence.

## Hardened against the supply chain

The modern threat is not only the bug in your own code; it is the compromised
dependency three steps upstream. The industry learned this the hard way with the
xz-utils backdoor — malicious changes slipped in through subtly inconsistent source
and a trust model that assumed maintainers and mirrors were honest and uncompromised.

glbx is built for a world where that assumption no longer holds. Because every
input is pinned to exact content and verified by hash, because the full history of
every package's source is recorded and reviewable, and because builds are isolated
and reproducible, there is no silent path for a tampered binary or a swapped source
tree to reach a shipped image unnoticed. A supply-chain attack has to get past a
system where every byte is pinned, every source is tracked, and every output is
independently reproducible — not past a chain of implicit trust.

## A faster path for the people who build it

The guarantees above are what the system *produces*. Just as important is what it
is like to *work on* — and here the vision is as much about Garden Linux's own
maintainers as about anyone downstream.

Consider the everyday task of changing one package and seeing whether the change
works in a real image. Without a unified build system, that loop is long and
indirect. You edit the package's own git repository — and if the package was
previously mirrored straight from Debian rather than built from source, you first
have to create that repository and stand the package up from scratch. Then there
is no direct way to try the result in an image: you push the change, perhaps wait
on a pull-request review, let CI build and publish the binary, let an APT
repository pick that binary up, and only then can you build a disk image that uses
it and finally check whether your change does what you intended, end to end.
Local workarounds that shortcut this exist, but all of them are awkward and
fragile.

glbx collapses that loop. Because an image is just the top of a build graph, you
change a package's source and build the image — locally, from one tool — and the
engine rebuilds exactly the affected packages and nothing else, pulling everything
unchanged straight from the cache. The thing you are testing is the thing you
would ship, produced the same way, with no detour through publishing, archives, or
review before you can even see whether it works. A change goes from idea to a
booting image in a single, fast, local step.

## Backports without the scramble

The same shift pays off most exactly when the stakes are highest: shipping a
security fix to an older release line under time pressure.

Today a backport can turn into far more than a version bump. If the release in
question used a version of the package that was mirrored directly from Debian, no
from-source build setup for that package exists in that release at all. Backporting
a fix then means doing all the first-time work of converting the package from
mirrored to built-from-source — and any problem that surfaces, a misconfigured
build dependency, a packaging quirk, an unreproducible step, surfaces *for the
first time on the critical path*, with a serious CVE and a clock running.

Building everything from source all the time moves that work to where it belongs:
into calm, day-to-day operations. Every package is already configured to build from
source, already exercised, already reproducible — so a backport is what it should
be, a change of source version against a setup that already works, rather than a
scramble to stand a package up from nothing under the worst possible deadline. The
hard problems get found and fixed in the quiet, and the time-sensitive response is
short because the groundwork was laid long before it was needed.

For a team, that is a dramatically lighter maintenance burden. For anyone
accountable for how quickly a critical vulnerability can be patched across every
supported release, it is a shorter, more predictable CVE-response timeline — by
design rather than by heroics.

## Sovereignty: a foundation you own

Taken together, these properties add up to something larger than a build tool. They
are the basis for genuine **digital sovereignty** over your operating system. When
every image is reproducible from version-controlled sources you hold, built by a
pipeline you control, with a complete and verifiable account of its contents, you
are no longer dependent on anyone else's infrastructure or anyone else's word for
the integrity of what you run. You can rebuild it, audit it, and prove it — on your
own terms, indefinitely.

That is the difference between consuming a distribution and owning your platform.

## Standing on Debian's shoulders

None of this comes at the cost of familiarity. Garden Linux is not a bespoke,
learn-everything-again system. It keeps the runtime world people already trust —
the standard filesystem layout, glibc and the GNU userland, `dpkg`-managed
packages, ordinary binaries that behave exactly as they do on any mainstream Linux
system. It carries the leverage of a vast, mature package ecosystem and the decades
of maintainer expertise behind it.

What glbx changes is not the result but the *discipline* of how that result is
built: builds as pure functions of their inputs, content-addressed identity,
isolation as an invariant, reproducibility by construction — the rigor that
functional build systems like Nix pioneered, applied to a thoroughly conventional
distribution. The combination is the whole point: the familiarity and reach of an
established Linux distribution, with the provenance, reproducibility, and
supply-chain guarantees usually reserved for a purpose-built system — and neither
bought at the other's expense.

## The ideals, stated plainly

Everything above rests on four commitments. They are not features layered on after
the fact; the architecture is chosen so that they hold by construction.

- **From source.** Every binary that lands in a shipped image is compiled from
  source under control. No opaque, mirrored binary leaks into the output.
- **Reproducible.** Identical inputs produce identical outputs, independent of when
  or where the build runs. A rebuild of unchanged inputs is a cache lookup, and two
  builders, continents apart, converge on the same bytes.
- **Hermetic.** A build sees only its declared inputs. The host is shielded from
  the build and the build from the host, so a result never depends on the machine
  that happened to run it.
- **Traceable.** Any file in any image can be followed back through the artifact
  that produced it to the version-controlled source and the exact inputs it was
  built from. Nothing in the output is unaccounted for.

These are the promises. The rest of this book is how glbx keeps them.
