/**
 * SkyDemonConnect's G1000 route plugin.
 *
 * A global plugin for the MFD of Working Title's G1000 NXi, both versions in
 * MSFS 2024: v2 (target AS1000v2_MFD) and v1, the copy of MSFS 2020's
 * (AS1000_MFD). Every few seconds it asks SkyDemonConnect on this PC for the
 * route last saved in SkyDemon, and loads it into the active flight plan: on
 * the ground near its start right away, otherwise, if saved during the
 * flight, after asking.
 *
 * Plain JavaScript on purpose: it uses the avionics framework the G1000
 * already loaded (window.msfssdk, window.garminsdk), so it needs no build.
 */
(function () {
  "use strict";

  const sdk = window.msfssdk;
  const garmin = window.garminsdk;
  if (!sdk || typeof sdk.registerPlugin !== "function" || !sdk.AvionicsPlugin) {
    console.error("[SkyDemonConnect] no avionics framework, plugin not loaded");
    return;
  }

  // SkyDemonConnect writes the ID of this plugin's files here when it
  // installs it, and says when it installed another one since MSFS started.
  const PLUGIN_ID = "@SKYDEMON_CONNECT_PLUGIN_ID@";
  const PLUGIN_HEADER = "Skydemonconnect-Plugin";
  const ROUTE_URL =
    "http://127.0.0.1:49080/route?plugin=" + encodeURIComponent(PLUGIN_ID);
  // The G1000 loads the EFB's route 2 s after the flight has started, which
  // would replace ours: start asking only after that.
  const AFTER_START_MS = 8000;
  const POLL_MS = 3000;
  // Failed connections are costly for the sim's browser engine: ask less
  // often while SkyDemonConnect isn't running.
  const RETRY_MS = 30000;
  const MESSAGE_MS = 12000;
  // A dialog that ends without telling, as v1's may with CLR, asks no more
  // after this: the next route is asked about again.
  const DIALOG_MS = 120000;
  const METERS_PER_NM = 1852;
  // SkyDemon's aerodrome positions and the sim's differ by a few hundred
  // metres.
  const AERODROME_MATCH_M = 1 * METERS_PER_NM;
  // On the ground this close to its start, a route is loaded without asking.
  const NEAR_START_M = 5 * METERS_PER_NM;
  const EARTH_RADIUS_M = 6371008.8;

  let messageTimer;

  /** Shows text on the MFD for a few seconds, and logs it. */
  function message(text, isError) {
    (isError ? console.error : console.log)("[SkyDemonConnect] " + text);

    let el = document.getElementById("skydemon-connect-message");
    if (!el) {
      el = document.createElement("div");
      el.id = "skydemon-connect-message";
      el.style.cssText = [
        "position:absolute",
        "left:50%",
        "top:7%",
        "transform:translateX(-50%)",
        "max-width:80%",
        "padding:6px 14px",
        "background:rgba(0,0,0,0.85)",
        "border:2px solid",
        "font-size:22px",
        "font-family:Roboto, sans-serif",
        "text-align:center",
        "z-index:10000",
        "pointer-events:none",
      ].join(";");
      document.body.appendChild(el);
    }

    el.style.borderColor = el.style.color = isError ? "#ffff00" : "#00ffff";
    el.textContent = "SkyDemonConnect: " + text;
    el.style.display = "block";
    clearTimeout(messageTimer);
    messageTimer = setTimeout(() => {
      el.style.display = "none";
    }, MESSAGE_MS);
  }

  function escapeHtml(s) {
    return String(s).replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  }

  function distanceM(lat1, lon1, lat2, lon2) {
    const rad = Math.PI / 180;
    const dLat = (lat2 - lat1) * rad;
    const dLon = (lon2 - lon1) * rad;
    const a =
      Math.sin(dLat / 2) ** 2 +
      Math.cos(lat1 * rad) * Math.cos(lat2 * rad) * Math.sin(dLon / 2) ** 2;
    return 2 * EARTH_RADIUS_M * Math.asin(Math.min(1, Math.sqrt(a)));
  }

  /** The ident of a facility's ICAO string: type, region (2), airport (4), ident. */
  function ident(fac) {
    return fac.icao.substring(7).trim();
  }

  /** The sim's airport nearest to p within AERODROME_MATCH_M, or null. */
  async function nearestAirport(fms, p) {
    const session = await fms.facLoader.startNearestSearchSession(
      sdk.FacilitySearchType.Airport,
    );
    const found = await session.searchNearest(
      p.lat,
      p.lon,
      AERODROME_MATCH_M,
      5,
    );

    let best = null;
    let bestM = Infinity;
    for (const icao of found.added) {
      const fac = await fms.facLoader.getFacility(
        sdk.FacilityType.Airport,
        icao,
      );
      const m = fac ? distanceM(p.lat, p.lon, fac.lat, fac.lon) : Infinity;
      if (m < bestM) {
        best = fac;
        bestM = m;
      }
    }

    return best;
  }

  /**
   * The route's points with their airports: SkyDemon saves positions only.
   * A first or last point that is an airport becomes the departure or the
   * destination, every other airport a waypoint, every other point a user
   * waypoint at its position.
   */
  async function resolve(fms, route) {
    const legs = [];
    for (const p of route.points) {
      legs.push({
        lat: p.lat,
        lon: p.lon,
        airport: p.aerodrome ? await nearestAirport(fms, p) : null,
      });
    }

    const departure = legs[0].airport ? legs.shift().airport : null;
    const destination =
      legs.length > 0 && legs[legs.length - 1].airport
        ? legs.pop().airport
        : null;

    return { departure, destination, legs };
  }

  function altitude(feet) {
    return feet > 0
      ? { __Type: "JS_FlightAltitude", altitude: feet, isFlightLevel: false }
      : null;
  }

  /**
   * v2: hands the route to the loader the G1000 uses for the EFB's Send to
   * Avionics, as a route the EFB could have sent.
   */
  async function loadV2(fms, route, plan) {
    const R = sdk.FlightPlanRouteUtils;

    const r = R.emptyRoute();
    if (plan.departure) {
      r.departureAirport = plan.departure.icaoStruct;
    }
    if (plan.destination) {
      r.destinationAirport = plan.destination.icaoStruct;
    }
    r.cruiseAltitude = altitude(route.levelFt);
    r.isVfr = !!route.vfr;

    for (const p of plan.legs) {
      const leg = R.emptyEnrouteLeg();
      if (p.airport) {
        leg.fixIcao = p.airport.icaoStruct;
      } else {
        leg.hasLatLon = true;
        leg.lat = p.lat;
        leg.lon = p.lon;
      }
      leg.altitude = altitude(route.levelFt);
      r.enroute.push(leg);
    }

    const loader = new garmin.GarminPrimaryFlightPlanRouteLoader(fms, {
      userFacilityScope: "NXI", // as the G1000's own, so its map shows them
      userFacilityIdentPrefix: "SKD",
      allowRnpArApproaches: false,
    });

    const ok = await loader.loadRoute(r);
    // As v1 below: in flight, on to the leg nearest to the aircraft.
    if (
      ok &&
      SimVar.GetSimVarValue("SIM ON GROUND", "bool") === 0 &&
      typeof fms.activateNearestLeg === "function"
    ) {
      fms.activateNearestLeg();
    }
    return ok;
  }

  /**
   * v1 has no route loader: builds the plan leg by leg. Returns true, or
   * the number of points the G1000 refused.
   */
  async function loadV1(fms, route, plan) {
    await fms.emptyPrimaryFlightPlan();

    const fpl = fms.getPrimaryFlightPlan();
    if (plan.departure) {
      fms.setOrigin(plan.departure);
    }

    let enroute = -1;
    for (const segment of fpl.segmentsOfType(
      sdk.FlightPlanSegmentType.Enroute,
    )) {
      enroute = segment.segmentIndex;
      break;
    }
    if (enroute < 0) {
      throw new Error("no enroute segment");
    }

    let user = 0;
    let skipped = 0;
    for (const p of plan.legs) {
      let fac = p.airport;
      if (!fac) {
        user++;
        // ICAO string: type, region (2), airport (4), ident (5): SKD01
        // to SKDZZ, in base 36 so that 1295 points fit.
        const icao =
          "U      SKD" + user.toString(36).toUpperCase().padStart(2, "0");
        if (icao.length > 12) {
          throw new Error("more than 1295 points");
        }
        fac = sdk.UserFacilityUtils.createFromLatLon(icao, p.lat, p.lon, true);
        fms.addUserFacility(fac);
      }
      if (fms.insertWaypoint(enroute, fac) === false) {
        skipped++;
      }
    }

    if (plan.destination) {
      fms.setDestination(plan.destination);
    }
    await fpl.calculate(0);

    if (SimVar.GetSimVarValue("SIM ON GROUND", "bool") === 0) {
      fms.activateNearestLeg();
    }

    return skipped > 0 ? skipped : true;
  }

  class SkyDemonConnectRoutePlugin extends sdk.AvionicsPlugin {
    onInstalled() {
      this.version =
        garmin && garmin.GarminPrimaryFlightPlanRouteLoader ? "v2" : "v1";
      // lastId is the route last loaded or asked about, pending the one a
      // dialog asks about.
      this.lastId = null;
      this.pending = null;
      // first: no route seen yet. A route saved before the flight is
      // often an old one, for another airport: only offered near its start.
      this.first = true;
      this.failing = false;
      this.warned = false;
      // retried is the route asked for again after a failed load.
      this.retried = null;
      // loads runs one load after the other: v1's is many steps.
      this.loads = Promise.resolve();
      console.log(
        "[SkyDemonConnect] route plugin installed in the G1000 " + this.version,
      );
      flightStarted().then(() => this.poll());
    }

    async poll() {
      let next = POLL_MS;

      try {
        const resp = await fetch(ROUTE_URL, { cache: "no-store" });
        if (resp.status !== 404 && !resp.ok) {
          throw new Error("HTTP " + resp.status);
        }

        if (this.failing) {
          this.failing = false;
          console.log("[SkyDemonConnect] connected");
        }

        const installed = resp.headers.get(PLUGIN_HEADER);
        if (installed && installed !== PLUGIN_ID && !this.warned) {
          this.warned = true;
          message("updated; restart MSFS to update its plugin as well", true);
        }

        // 404: no route saved yet.
        const route = resp.ok ? await resp.json() : null;
        const first = this.first;
        this.first = false;
        if (route && route.id !== this.lastId) {
          this.lastId = route.id;
          try {
            await this.offer(route, first);
          } catch (e) {
            // Not SkyDemonConnect's fault: say so, and ask again once.
            this.pending = null;
            message(route.name + " not offered: " + e, true);
            this.retry(route);
          }
        }
      } catch (e) {
        next = RETRY_MS;
        if (!this.failing) {
          this.failing = true;
          console.log(
            "[SkyDemonConnect] no route from " + ROUTE_URL + ": " + e,
          );
        }
      }

      setTimeout(() => this.poll(), next);
    }

    /**
     * Loads a new route near its start on the ground. Elsewhere it asks first,
     * unless the route is the one found at the start of the flight.
     */
    async offer(route, first) {
      const onGround = SimVar.GetSimVarValue("SIM ON GROUND", "bool") !== 0;
      const start = route.points[0];
      const fromStart = distanceM(
        SimVar.GetSimVarValue("PLANE LATITUDE", "degrees"),
        SimVar.GetSimVarValue("PLANE LONGITUDE", "degrees"),
        start.lat,
        start.lon,
      );

      if (onGround && fromStart <= NEAR_START_M) {
        // A dialog still open for an older route then loads nothing.
        this.pending = null;
        await this.load(route);
        return;
      }

      if (first) {
        console.log(
          "[SkyDemonConnect] route " +
            route.name +
            " starts " +
            Math.round(fromStart / METERS_PER_NM) +
            " NM away: not offered",
        );
        return;
      }

      // A dialog already open loads the newest route when accepted.
      const asking = this.pending !== null;
      this.pending = route;
      if (asking) {
        return;
      }

      const views = this.binder.viewService;
      if (!views) {
        this.pending = null;
        message("new route " + route.name + " not loaded: no dialog", true);
        return;
      }

      const dialog = views.open("MessageDialog", true).setInput({
        inputString:
          "Load the SkyDemon route " +
          escapeHtml(route.name) +
          "?<br>It replaces the active flight plan.",
        hasRejectButton: true,
        confirmButtonText: "LOAD",
        rejectButtonText: "KEEP",
      });
      // The G1000 has one MessageDialog for all its questions: the handlers
      // go with the first answer, or when it is closed with CLR, which
      // answers nothing, so that they never answer another question and
      // the next route is asked about again.
      const onClose = dialog.onClose;
      // Closing may come before the answer: the answer still counts.
      const closed = () => setTimeout(done, 0);
      let answered = false;
      const done = () => {
        if (answered) {
          return;
        }
        answered = true;
        dialog.onAccept.off(onAccept);
        if (onClose && typeof onClose.off === "function") {
          onClose.off(closed);
        }
        this.pending = null;
      };
      const onAccept = (sender, accept) => {
        const latest = this.pending;
        done();
        if (accept && latest) {
          this.load(latest);
        }
      };
      dialog.onAccept.on(onAccept);
      if (onClose && typeof onClose.on === "function") {
        onClose.on(closed);
      } else {
        setTimeout(done, DIALOG_MS);
      }
    }

    /** Loads route once the loads before it are done, failed or not. */
    load(route) {
      const run = () => this.loadNow(route);
      this.loads = this.loads.then(run, run);
      return this.loads;
    }

    /** Asks for route again at the next poll, once: e.g. the FMS wasn't ready. */
    retry(route) {
      if (this.retried !== route.id && this.lastId === route.id) {
        this.retried = route.id;
        this.lastId = null;
      }
    }

    async loadNow(route) {
      try {
        const fms = this.binder.fms;
        if (!fms || !fms.hasPrimaryFlightPlan()) {
          message("no flight plan to load " + route.name + " into", true);
          this.retry(route);
          return;
        }

        const plan = await resolve(fms, route);
        const name =
          plan.departure && plan.destination
            ? ident(plan.departure) + "-" + ident(plan.destination)
            : route.name;

        const ok =
          this.version === "v2"
            ? await loadV2(fms, route, plan)
            : await loadV1(fms, route, plan);
        if (ok === true) {
          message(name + " loaded");
        } else if (typeof ok === "number") {
          message(name + " loaded without " + ok + " of its points", true);
        } else {
          message(name + " not loaded: the G1000 refused it", true);
          this.retry(route);
        }
      } catch (e) {
        message(route.name + " not loaded: " + e, true);
        this.retry(route);
        if (e && e.stack) {
          console.error(e.stack);
        }
      }
    }
  }

  /** Waits until the flight has started and the G1000 has loaded the EFB's route. */
  async function flightStarted() {
    if (sdk.GameStateProvider && sdk.GameState && sdk.Wait) {
      await sdk.Wait.awaitSubscribable(
        sdk.GameStateProvider.get(),
        (s) => s === sdk.GameState.ingame,
        true,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, AFTER_START_MS));
  }

  sdk.registerPlugin(SkyDemonConnectRoutePlugin);
})();
