def execute(data):
    encode = lambda fields: "".join(x for x in fields)
    return {"authorized": encode(data["signed"]) == encode(data["requested"])}
